package tui

import (
	"context"
	"errors"
	"path/filepath"
	"strings"
	"testing"
	"time"

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
	if model.mode != modeHelp || !strings.Contains(model.status, "/models") {
		t.Fatalf("help mode=%v status=%q, want help command summary", model.mode, model.status)
	}

	model.requestQuit()
	if !model.quitting {
		t.Fatalf("quitting = false, want true")
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
