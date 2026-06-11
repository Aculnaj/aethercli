package commands

import (
	"bytes"
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/Aculnaj/aethercli/internal/api"
)

type memorySecretStore struct {
	key string
}

func (m *memorySecretStore) Get() (string, error) {
	if m.key == "" {
		return "", ErrSecretNotFoundForTest
	}
	return m.key, nil
}

func (m *memorySecretStore) Set(value string) error {
	m.key = value
	return nil
}

func (m *memorySecretStore) Delete() error {
	m.key = ""
	return nil
}

var ErrSecretNotFoundForTest = errSecretNotFoundForTest{}

type errSecretNotFoundForTest struct{}

func (errSecretNotFoundForTest) Error() string { return "not found" }
func (errSecretNotFoundForTest) Is(target error) bool {
	return target.Error() == "secret not found"
}

type fakeAPIClient struct {
	chatRequest  api.ChatRequest
	chatContent  string
	streamDeltas []string
	models       []api.Model
}

func (f *fakeAPIClient) Chat(ctx context.Context, req api.ChatRequest) (api.ChatResponse, error) {
	f.chatRequest = req
	return api.ChatResponse{Model: req.Model, Content: f.chatContent}, nil
}

func (f *fakeAPIClient) StreamChat(ctx context.Context, req api.ChatRequest, onDelta func(string) error) error {
	f.chatRequest = req
	for _, delta := range f.streamDeltas {
		if err := onDelta(delta); err != nil {
			return err
		}
	}
	if len(f.streamDeltas) > 0 {
		return nil
	}
	return onDelta(f.chatContent)
}

func (f *fakeAPIClient) Models(ctx context.Context) ([]api.Model, error) {
	return f.models, nil
}

func TestAskUsesExplicitModelAndJSONOutput(t *testing.T) {
	configPath := writeConfig(t, `{"base_url":"https://api.aetherapi.dev/v1","default_model":"gpt-4o"}`)
	store := &memorySecretStore{key: "sk-aetherapi-test"}
	fakeClient := &fakeAPIClient{chatContent: "answer"}
	var out bytes.Buffer

	cmd := NewRootCommand(Deps{
		ConfigPath: configPath,
		Secrets:    store,
		In:         strings.NewReader(""),
		Out:        &out,
		Err:        &bytes.Buffer{},
		ClientFactory: func(baseURL, apiKey string) APIClient {
			if baseURL != "https://api.aetherapi.dev/v1" {
				t.Fatalf("baseURL = %q", baseURL)
			}
			if apiKey != "sk-aetherapi-test" {
				t.Fatalf("apiKey = %q", apiKey)
			}
			return fakeClient
		},
	})
	cmd.SetArgs([]string{"ask", "hello", "--model", "claude-sonnet-4-6", "--json"})

	if err := cmd.Execute(); err != nil {
		t.Fatalf("Execute returned error: %v", err)
	}
	if fakeClient.chatRequest.Model != "claude-sonnet-4-6" {
		t.Fatalf("model = %q", fakeClient.chatRequest.Model)
	}
	if fakeClient.chatRequest.Prompt != "hello" {
		t.Fatalf("prompt = %q", fakeClient.chatRequest.Prompt)
	}

	var payload map[string]string
	if err := json.Unmarshal(out.Bytes(), &payload); err != nil {
		t.Fatalf("json output invalid: %v\n%s", err, out.String())
	}
	if payload["content"] != "answer" {
		t.Fatalf("content = %q", payload["content"])
	}
}

func TestModelsJSONFiltersChatModels(t *testing.T) {
	configPath := writeConfig(t, `{"base_url":"https://api.aetherapi.dev/v1","default_model":"gpt-4o"}`)
	fakeClient := &fakeAPIClient{models: []api.Model{
		{ID: "image", Endpoint: "/v1/images/generations"},
		{ID: "chat", Endpoint: "/v1/chat/completions", OwnedBy: "OpenAI"},
	}}
	var out bytes.Buffer

	cmd := NewRootCommand(Deps{
		ConfigPath: configPath,
		Secrets:    &memorySecretStore{key: "sk-aetherapi-test"},
		In:         strings.NewReader(""),
		Out:        &out,
		Err:        &bytes.Buffer{},
		ClientFactory: func(baseURL, apiKey string) APIClient {
			return fakeClient
		},
	})
	cmd.SetArgs([]string{"models", "--json"})

	if err := cmd.Execute(); err != nil {
		t.Fatalf("Execute returned error: %v", err)
	}

	var got []api.Model
	if err := json.Unmarshal(out.Bytes(), &got); err != nil {
		t.Fatalf("json output invalid: %v\n%s", err, out.String())
	}
	if len(got) != 1 || got[0].ID != "chat" {
		t.Fatalf("models output = %#v", got)
	}
}

func TestRootNoArgsRunsSetupAndAsksPrompt(t *testing.T) {
	store := &memorySecretStore{}
	fakeClient := &fakeAPIClient{chatContent: "interactive answer"}
	var out bytes.Buffer
	var errOut bytes.Buffer

	cmd := NewRootCommand(Deps{
		ConfigPath: filepath.Join(t.TempDir(), "config.json"),
		Secrets:    store,
		In: strings.NewReader(strings.Join([]string{
			"Bearer sk-aetherapi-test",
			"claude-sonnet-4-6",
			"hello interactively",
			"",
		}, "\n")),
		Out: &out,
		Err: &errOut,
		ClientFactory: func(baseURL, apiKey string) APIClient {
			return fakeClient
		},
	})
	cmd.SetArgs([]string{})

	if err := cmd.Execute(); err != nil {
		t.Fatalf("Execute returned error: %v", err)
	}
	if store.key != "sk-aetherapi-test" {
		t.Fatalf("stored key = %q, want normalized key", store.key)
	}
	if fakeClient.chatRequest.Model != "claude-sonnet-4-6" {
		t.Fatalf("model = %q", fakeClient.chatRequest.Model)
	}
	if fakeClient.chatRequest.Prompt != "hello interactively" {
		t.Fatalf("prompt = %q", fakeClient.chatRequest.Prompt)
	}
	if !strings.Contains(out.String(), "interactive answer") {
		t.Fatalf("stdout = %q, want answer", out.String())
	}
}

type flushRecordingWriter struct {
	bytes.Buffer
	flushes int
}

func (w *flushRecordingWriter) Flush() error {
	w.flushes++
	return nil
}

func TestAskStreamFlushesEachDelta(t *testing.T) {
	configPath := writeConfig(t, `{"base_url":"https://api.aetherapi.dev/v1","default_model":"gpt-4o"}`)
	fakeClient := &fakeAPIClient{streamDeltas: []string{"hel", "lo"}}
	out := &flushRecordingWriter{}

	cmd := NewRootCommand(Deps{
		ConfigPath: configPath,
		Secrets:    &memorySecretStore{key: "sk-aetherapi-test"},
		In:         strings.NewReader(""),
		Out:        out,
		Err:        &bytes.Buffer{},
		ClientFactory: func(baseURL, apiKey string) APIClient {
			return fakeClient
		},
	})
	cmd.SetArgs([]string{"ask", "hello", "--stream"})

	if err := cmd.Execute(); err != nil {
		t.Fatalf("Execute returned error: %v", err)
	}
	if got := out.String(); got != "hello\n" {
		t.Fatalf("stdout = %q, want streamed content plus newline", got)
	}
	if out.flushes != len(fakeClient.streamDeltas)+1 {
		t.Fatalf("flushes = %d, want %d", out.flushes, len(fakeClient.streamDeltas)+1)
	}
}

func writeConfig(t *testing.T, raw string) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "config.json")
	if err := os.WriteFile(path, []byte(raw), 0o600); err != nil {
		t.Fatal(err)
	}
	return path
}
