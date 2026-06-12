package tui

import (
	"context"
	"errors"
	"path/filepath"
	"strings"
	"testing"
	"time"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"

	"github.com/Aculnaj/aethercli/internal/api"
	"github.com/Aculnaj/aethercli/internal/config"
	"github.com/Aculnaj/aethercli/internal/session"
)

type fakeClient struct {
	models       []api.Model
	streamDeltas []string
	streamErr    error
	request      api.ChatRequest
}

func (f *fakeClient) StreamChat(ctx context.Context, req api.ChatRequest, onDelta func(string) error) error {
	f.request = req
	for _, delta := range f.streamDeltas {
		if err := onDelta(delta); err != nil {
			return err
		}
	}
	return f.streamErr
}

func (f *fakeClient) Models(ctx context.Context) ([]api.Model, error) {
	return f.models, nil
}

func TestParseSlashCommandRecognizesV1Commands(t *testing.T) {
	tests := map[string]slashCommand{
		"/models":        {name: "models"},
		"/model gpt-4o":  {name: "model", arg: "gpt-4o"},
		"/sessions":      {name: "sessions"},
		"/resume abc123": {name: "resume", arg: "abc123"},
		"/new":           {name: "new"},
		"/clear":         {name: "clear"},
		"/usage":         {name: "usage"},
		"/help":          {name: "help"},
		"/quit":          {name: "quit"},
	}

	for input, want := range tests {
		got := parseSlashCommand(input)
		if got != want {
			t.Fatalf("parseSlashCommand(%q) = %#v, want %#v", input, got, want)
		}
	}
}

func TestModelsCommandFiltersChatModelsAndSelectionSetsActiveModel(t *testing.T) {
	client := &fakeClient{models: []api.Model{
		{ID: "image", Endpoint: "/v1/images/generations"},
		{ID: "chat-a", Endpoint: "/v1/chat/completions"},
		{ID: "chat-b", SupportedEndpoints: []string{"/chat/completions"}},
	}}
	model := newTestModel(t, client)

	if err := model.showModels(context.Background()); err != nil {
		t.Fatalf("showModels returned error: %v", err)
	}
	if len(model.models) != 2 {
		t.Fatalf("models = %#v, want only chat models", model.models)
	}
	model.modelCursor = 1
	model.selectCurrentModel()

	if model.activeModel != "chat-b" {
		t.Fatalf("active model = %q, want selected chat model", model.activeModel)
	}
	if model.mode != modeChat {
		t.Fatalf("mode = %v, want chat mode after selection", model.mode)
	}
}

func TestModelsViewFitsTerminalWidthAndShowsRows(t *testing.T) {
	model := newTestModel(t, &fakeClient{models: []api.Model{
		{ID: "claude-sonnet-4-6", Endpoint: "/v1/chat/completions", OwnedBy: "Anthropic", Context: "200k"},
		{ID: "kimi-k2", Endpoint: "/v1/chat/completions", OwnedBy: "Moonshot", Context: "128k"},
	}})
	model.width = 60
	model.height = 18

	if err := model.showModels(context.Background()); err != nil {
		t.Fatalf("showModels returned error: %v", err)
	}
	view := model.View()
	lines := strings.Split(view, "\n")
	for _, want := range []string{"Models", "claude-sonnet-4-6", "kimi-k2"} {
		if !strings.Contains(view, want) {
			t.Fatalf("view = %q, want visible model content %q", view, want)
		}
	}
	if len(lines) > model.height {
		t.Fatalf("view line count = %d > terminal height %d:\n%s", len(lines), model.height, view)
	}
	for _, line := range lines {
		if width := lipgloss.Width(line); width > model.width {
			t.Fatalf("line width = %d > terminal width %d: %q", width, model.width, line)
		}
	}
}

func TestModelsCommandSearchesAndScrollsLongModelList(t *testing.T) {
	models := []api.Model{}
	for i := range 30 {
		id := "chat-model-" + string(rune('a'+i%26))
		if i == 20 {
			id = "claude-sonnet-4-6"
		}
		models = append(models, api.Model{ID: id, Endpoint: "/v1/chat/completions"})
	}
	model := newTestModel(t, &fakeClient{models: models})
	model.height = 9

	if err := model.showModels(context.Background()); err != nil {
		t.Fatalf("showModels returned error: %v", err)
	}
	for i := 0; i < 20; i++ {
		model.updateModelsKey(tea.KeyMsg{Type: tea.KeyDown})
	}
	rendered := model.renderModels()
	if strings.Contains(rendered, "chat-model-a") || !strings.Contains(rendered, "claude-sonnet-4-6") {
		t.Fatalf("rendered models = %q, want scrolled window around selected model", rendered)
	}

	for _, r := range "sonnet" {
		model.updateModelsKey(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{r}})
	}
	filtered := model.filteredModels()
	if len(filtered) != 1 || filtered[0].ID != "claude-sonnet-4-6" {
		t.Fatalf("filtered models = %#v, want sonnet match only", filtered)
	}
	if got := model.renderModels(); !strings.Contains(got, "Search: sonnet") || strings.Contains(got, "chat-model-b") {
		t.Fatalf("rendered filtered models = %q, want search UI with filtered result", got)
	}
}

func TestModelSearchAcceptsVimNavigationLettersAsQueryText(t *testing.T) {
	model := newTestModel(t, &fakeClient{models: []api.Model{
		{ID: "kimi-k2", Endpoint: "/v1/chat/completions"},
		{ID: "jamba-large", Endpoint: "/v1/chat/completions"},
	}})

	if err := model.showModels(context.Background()); err != nil {
		t.Fatalf("showModels returned error: %v", err)
	}
	model.updateModelsKey(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'k'}})
	if model.modelQuery != "k" {
		t.Fatalf("model query = %q, want k to be searchable text", model.modelQuery)
	}
	model.updateModelsKey(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'j'}})
	if model.modelQuery != "kj" {
		t.Fatalf("model query = %q, want j to be searchable text", model.modelQuery)
	}
}

func TestModelCommandWithoutArgumentShowsCurrentModelDetailsAndCanSwitch(t *testing.T) {
	client := &fakeClient{models: []api.Model{
		{
			ID:       "chat-a",
			Endpoint: "/v1/chat/completions",
			OwnedBy:  "Provider A",
			Context:  "64k",
			OurPrice: "$1 / 1M input tokens, $2 / 1M output tokens",
		},
		{
			ID:       "chat-b",
			Endpoint: "/v1/chat/completions",
			OwnedBy:  "Provider B",
			Context:  "200k",
			OurPrice: "$3 / 1M input tokens, $6 / 1M output tokens",
		},
	}}
	model := newTestModel(t, client)
	model.activeModel = "chat-b"

	if err := model.showCurrentModelDetails(context.Background()); err != nil {
		t.Fatalf("showCurrentModelDetails returned error: %v", err)
	}
	if model.modelCursor != 1 {
		t.Fatalf("model cursor = %d, want current model selected", model.modelCursor)
	}
	rendered := model.renderModels()
	for _, want := range []string{"Current model", "chat-b", "Provider B", "200k", "$3 / 1M input tokens"} {
		if !strings.Contains(rendered, want) {
			t.Fatalf("rendered model detail = %q, want %q", rendered, want)
		}
	}

	model.modelCursor = 0
	model.selectCurrentModel()
	if model.activeModel != "chat-a" {
		t.Fatalf("active model = %q, want switched model", model.activeModel)
	}
}

func TestSendPromptStreamsDeltasAndSavesAfterSuccess(t *testing.T) {
	client := &fakeClient{streamDeltas: []string{"hel", "lo"}}
	model := newTestModel(t, client)

	if err := model.sendPrompt(context.Background(), "say hello"); err != nil {
		t.Fatalf("sendPrompt returned error: %v", err)
	}
	if got := model.messages[len(model.messages)-1]; got.Role != "assistant" || got.Content != "hello" {
		t.Fatalf("last message = %#v, want streamed assistant response", got)
	}
	saved, err := model.store.Latest()
	if err != nil {
		t.Fatalf("load latest session: %v", err)
	}
	if len(saved.Messages) != 2 {
		t.Fatalf("saved messages = %d, want user and assistant", len(saved.Messages))
	}
	if saved.Messages[1].Content != "hello" {
		t.Fatalf("saved assistant = %q, want streamed response", saved.Messages[1].Content)
	}
	if model.status == "Saved." {
		t.Fatalf("status = %q, want neutral status after successful save", model.status)
	}
}

func TestStreamingViewDoesNotDuplicateStreamingStatus(t *testing.T) {
	model := newTestModel(t, &fakeClient{})
	model.streaming = true
	model.status = "Streaming..."

	view := model.View()
	if strings.Contains(view, "Streaming response...") {
		t.Fatalf("view = %q, want no duplicate streaming input label", view)
	}
	if count := strings.Count(view, "Streaming..."); count != 1 {
		t.Fatalf("view = %q, streaming status count = %d, want 1", view, count)
	}
}

func TestViewRendersFramedPanelsForNavigation(t *testing.T) {
	model := newTestModel(t, &fakeClient{})
	model.width = 90
	model.input.SetValue("/")

	view := model.View()
	for _, want := range []string{"Aether Chat", "Chat", "Commands", "Status", "Input"} {
		if !strings.Contains(view, want) {
			t.Fatalf("view = %q, want framed section %q", view, want)
		}
	}
}

func TestSendPromptKeepsUserMessageAndDoesNotSaveAssistantOnStreamError(t *testing.T) {
	client := &fakeClient{
		streamDeltas: []string{"partial"},
		streamErr:    errors.New("network down"),
	}
	model := newTestModel(t, client)

	if err := model.sendPrompt(context.Background(), "question"); err == nil {
		t.Fatalf("sendPrompt returned nil, want error")
	}
	if len(model.messages) != 1 || model.messages[0].Role != "user" {
		t.Fatalf("messages = %#v, want visible user message only", model.messages)
	}
	if model.status == "" || !strings.Contains(model.status, "network down") {
		t.Fatalf("status = %q, want stream error", model.status)
	}
	if _, err := model.store.Latest(); err == nil {
		t.Fatalf("latest session loaded, want no saved session after failed assistant response")
	}
}

func TestSessionCommandsLoadResumeNewClearHelpAndQuitState(t *testing.T) {
	client := &fakeClient{}
	model := newTestModel(t, client)

	first, err := model.store.New("gpt-4o", "old chat")
	if err != nil {
		t.Fatal(err)
	}
	model.store.Append(&first, "user", "old question")
	if err := model.store.Save(first); err != nil {
		t.Fatal(err)
	}

	if err := model.showSessions(); err != nil {
		t.Fatalf("showSessions returned error: %v", err)
	}
	if len(model.sessions) != 1 {
		t.Fatalf("sessions = %d, want one saved session", len(model.sessions))
	}
	if err := model.resumeSession(first.ID); err != nil {
		t.Fatalf("resumeSession returned error: %v", err)
	}
	if model.sessionID != first.ID || len(model.messages) != 1 {
		t.Fatalf("loaded session id=%q messages=%d, want saved session", model.sessionID, len(model.messages))
	}

	model.clearVisible()
	if len(model.messages) != 0 || model.sessionID != first.ID {
		t.Fatalf("clearVisible messages=%d session=%q, want hidden transcript only", len(model.messages), model.sessionID)
	}

	if err := model.newSession(); err != nil {
		t.Fatalf("newSession returned error: %v", err)
	}
	if model.sessionID != "" || len(model.messages) != 0 {
		t.Fatalf("new session id=%q messages=%d, want empty unsaved session", model.sessionID, len(model.messages))
	}

	model.showHelp()
	if model.mode != modeChat || !model.helpVisible || !strings.Contains(model.status, "/models") {
		t.Fatalf("help mode=%v visible=%v status=%q, want non-blocking help", model.mode, model.helpVisible, model.status)
	}
	updated, _ := model.updateKey(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'h'}})
	model = updated.(*Model)
	if model.helpVisible || model.input.Value() != "h" {
		t.Fatalf("help visible=%v input=%q, want help dismissed and input accepted", model.helpVisible, model.input.Value())
	}

	model.requestQuit()
	if !model.quitting {
		t.Fatalf("quitting = false, want true")
	}
}

func TestUsageCommandShowsCurrentSessionUsageAndDoesNotBlockInput(t *testing.T) {
	model := newTestModel(t, &fakeClient{})
	item, err := model.store.New("gpt-4o", "usage chat")
	if err != nil {
		t.Fatal(err)
	}
	model.store.Append(&item, "user", "hello usage")
	model.store.Append(&item, "assistant", "usage answer")
	if err := model.store.Save(item); err != nil {
		t.Fatal(err)
	}
	model.loadSession(item)

	model.showUsage()
	if !model.usageVisible {
		t.Fatalf("usage visible = false, want usage overlay")
	}
	rendered := model.renderUsage()
	for _, want := range []string{"Session usage", item.ID, "Messages: 2", "Estimated tokens:"} {
		if !strings.Contains(rendered, want) {
			t.Fatalf("usage = %q, want %q", rendered, want)
		}
	}

	updated, _ := model.updateKey(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'n'}})
	model = updated.(*Model)
	if model.usageVisible || model.input.Value() != "n" {
		t.Fatalf("usage visible=%v input=%q, want usage dismissed and input accepted", model.usageVisible, model.input.Value())
	}
}

func TestSlashInputShowsFilteredCommandSuggestions(t *testing.T) {
	model := newTestModel(t, &fakeClient{})

	model.input.SetValue("/")
	rendered := model.View()
	for _, want := range []string{"/models", "/model <id>", "/usage", "/help"} {
		if !strings.Contains(rendered, want) {
			t.Fatalf("view = %q, want slash suggestion %q", rendered, want)
		}
	}

	model.input.SetValue("/mo")
	rendered = model.View()
	if !strings.Contains(rendered, "/models") || !strings.Contains(rendered, "/model <id>") || strings.Contains(rendered, "/quit") {
		t.Fatalf("view = %q, want filtered model suggestions only", rendered)
	}

	model.input.SetValue("/mo")
	updated, _ := model.updateKey(tea.KeyMsg{Type: tea.KeyDown})
	model = updated.(*Model)
	updated, _ = model.updateKey(tea.KeyMsg{Type: tea.KeyTab})
	model = updated.(*Model)
	if model.input.Value() != "/model " {
		t.Fatalf("input = %q, want selected slash suggestion completed", model.input.Value())
	}
}

func TestSlashSuggestionArrowKeysWrapAround(t *testing.T) {
	model := newTestModel(t, &fakeClient{})
	model.input.SetValue("/")

	updated, _ := model.updateKey(tea.KeyMsg{Type: tea.KeyUp})
	model = updated.(*Model)
	suggestions := model.currentSlashSuggestions()
	if model.slashCursor != len(suggestions)-1 {
		t.Fatalf("slash cursor = %d, want up from first suggestion to wrap to last", model.slashCursor)
	}

	updated, _ = model.updateKey(tea.KeyMsg{Type: tea.KeyDown})
	model = updated.(*Model)
	if model.slashCursor != 0 {
		t.Fatalf("slash cursor = %d, want down from last suggestion to wrap to first", model.slashCursor)
	}
}

func TestEnterConfirmsSingleSlashCommandSuggestion(t *testing.T) {
	model := newTestModel(t, &fakeClient{})
	model.input.SetValue("/he")

	updated, cmd := model.updateKey(tea.KeyMsg{Type: tea.KeyEnter})
	model = updated.(*Model)
	if cmd != nil {
		t.Fatalf("cmd = %#v, want no async command for help suggestion", cmd)
	}
	if !model.helpVisible {
		t.Fatalf("help visible = false, want Enter to execute the only matching slash command")
	}
	if model.input.Value() != "" {
		t.Fatalf("input = %q, want confirmed command to clear input", model.input.Value())
	}
}

func newTestModel(t *testing.T, client *fakeClient) *Model {
	t.Helper()
	configPath := filepath.Join(t.TempDir(), "config.json")
	if err := config.Save(configPath, config.Config{BaseURL: config.DefaultBaseURL, DefaultModel: "gpt-4o"}); err != nil {
		t.Fatal(err)
	}
	store, err := session.NewStore(configPath, func() time.Time {
		return time.Date(2026, 6, 12, 10, 30, 0, 0, time.UTC)
	})
	if err != nil {
		t.Fatal(err)
	}
	return NewModel(Options{
		ConfigPath: configPath,
		Config:     config.Config{BaseURL: config.DefaultBaseURL, DefaultModel: "gpt-4o"},
		APIKey:     "sk-aetherapi-test",
		Model:      "gpt-4o",
		Store:      &store,
		ClientFactory: func(baseURL, apiKey string) Client {
			return client
		},
		Now: func() time.Time {
			return time.Date(2026, 6, 12, 10, 30, 0, 0, time.UTC)
		},
	})
}
