package api

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestChatPostsOpenAICompatibleRequest(t *testing.T) {
	var captured struct {
		Model    string `json:"model"`
		Messages []struct {
			Role    string `json:"role"`
			Content string `json:"content"`
		} `json:"messages"`
		Temperature float64 `json:"temperature"`
		MaxTokens   int     `json:"max_tokens"`
		Stream      bool    `json:"stream"`
	}

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/chat/completions" {
			t.Fatalf("path = %q, want /chat/completions", r.URL.Path)
		}
		if r.Method != http.MethodPost {
			t.Fatalf("method = %q, want POST", r.Method)
		}
		if got := r.Header.Get("Authorization"); got != "Bearer sk-aetherapi-test" {
			t.Fatalf("Authorization = %q", got)
		}
		if err := json.NewDecoder(r.Body).Decode(&captured); err != nil {
			t.Fatalf("decode request: %v", err)
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"choices":[{"message":{"content":"hello from aether"}}]}`))
	}))
	defer server.Close()

	temp := 0.7
	maxTokens := 256
	client := NewClient(ClientOptions{
		BaseURL:    server.URL,
		APIKey:     "sk-aetherapi-test",
		HTTPClient: server.Client(),
	})

	got, err := client.Chat(context.Background(), ChatRequest{
		Model:       "claude-sonnet-4-6",
		Prompt:      "hello",
		Temperature: &temp,
		MaxTokens:   &maxTokens,
	})
	if err != nil {
		t.Fatalf("Chat returned error: %v", err)
	}
	if got.Content != "hello from aether" {
		t.Fatalf("Chat content = %q", got.Content)
	}
	if captured.Model != "claude-sonnet-4-6" {
		t.Fatalf("model = %q", captured.Model)
	}
	if len(captured.Messages) != 1 || captured.Messages[0].Role != "user" || captured.Messages[0].Content != "hello" {
		t.Fatalf("messages = %#v", captured.Messages)
	}
	if captured.Temperature != temp {
		t.Fatalf("temperature = %v", captured.Temperature)
	}
	if captured.MaxTokens != maxTokens {
		t.Fatalf("max_tokens = %v", captured.MaxTokens)
	}
	if captured.Stream {
		t.Fatal("stream = true, want false by default")
	}
}

func TestStreamChatParsesServerSentEvents(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		_, _ = w.Write([]byte("data: {\"choices\":[{\"delta\":{\"content\":\"hel\"}}]}\n\n"))
		_, _ = w.Write([]byte("data: {\"choices\":[{\"delta\":{\"content\":\"lo\"}}]}\n\n"))
		_, _ = w.Write([]byte("data: [DONE]\n\n"))
	}))
	defer server.Close()

	client := NewClient(ClientOptions{
		BaseURL:    server.URL,
		APIKey:     "sk-aetherapi-test",
		HTTPClient: server.Client(),
	})

	var got strings.Builder
	err := client.StreamChat(context.Background(), ChatRequest{
		Model:  "claude-sonnet-4-6",
		Prompt: "hello",
	}, func(delta string) error {
		got.WriteString(delta)
		return nil
	})
	if err != nil {
		t.Fatalf("StreamChat returned error: %v", err)
	}
	if got.String() != "hello" {
		t.Fatalf("streamed content = %q, want hello", got.String())
	}
}

func TestFilterChatModels(t *testing.T) {
	models := []Model{
		{ID: "image-only", Endpoint: "/v1/images/generations"},
		{ID: "chat-endpoint", Endpoint: "/v1/chat/completions"},
		{ID: "chat-supported", Endpoint: "/v1/responses", SupportedEndpoints: []string{"/v1/chat/completions"}},
	}

	got := FilterChatModels(models)
	if len(got) != 2 {
		t.Fatalf("FilterChatModels returned %d models, want 2", len(got))
	}
	if got[0].ID != "chat-endpoint" || got[1].ID != "chat-supported" {
		t.Fatalf("FilterChatModels = %#v", got)
	}
}

func TestAPIErrorMapsKnownCodes(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusUnauthorized)
		_, _ = w.Write([]byte(`{"error":{"code":"invalid_api_key","message":"bad key"}}`))
	}))
	defer server.Close()

	client := NewClient(ClientOptions{
		BaseURL:    server.URL,
		APIKey:     "sk-aetherapi-test",
		HTTPClient: server.Client(),
	})

	_, err := client.Chat(context.Background(), ChatRequest{Model: "gpt-4o", Prompt: "hello"})
	var apiErr *APIError
	if !errors.As(err, &apiErr) {
		t.Fatalf("Chat error = %T, want *APIError", err)
	}
	if !strings.Contains(apiErr.UserMessage(), "invalid API key") {
		t.Fatalf("UserMessage = %q, want invalid API key guidance", apiErr.UserMessage())
	}
}
