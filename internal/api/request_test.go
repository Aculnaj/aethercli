package api

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

func TestSSEProtocolsErrorsAndTruncation(t *testing.T) {
	for _, tc := range []struct{ name, body, code string }{
		{"chat", "data: {\"choices\":[]}\n\ndata: [DONE]\n\n", ""},
		{"responses", "event: response.output_text.delta\ndata: {\"type\":\"response.output_text.delta\",\"delta\":\"hi\"}\n\nevent: response.completed\ndata: {\"type\":\"response.completed\"}\n\n", ""},
		{"messages", "event: message_stop\ndata: {\"type\":\"message_stop\"}\n\n", ""},
		{"CRLF_multiline", ": ping\r\ndata: {\r\ndata: \"choices\":[]\r\ndata: }\r\n\r\ndata: [DONE]\r\n\r\n", ""},
		{"unterminated_terminal", "data: [DONE]", ""},
		{"truncated", "data: {\"choices\":[]}\n\n", "stream_truncated"},
		{"empty", "", "stream_truncated"},
		{"invalid", "data: not-json\n\n", "invalid_stream"},
		{"chat_error", "data: {\"error\":{\"code\":\"sandbox_simulated_stream_error\",\"message\":\"partial failed\"}}\n\n", "sandbox_simulated_stream_error"},
		{"responses_error", "event: error\ndata: {\"type\":\"error\",\"code\":\"provider_error\",\"message\":\"failed\"}\n\n", "provider_error"},
		{"responses_failed", "data: {\"type\":\"response.failed\",\"response\":{\"error\":{\"code\":\"response_failure\"}}}\n\n", "response_failure"},
		{"responses_incomplete", "data: {\"type\":\"response.incomplete\"}\n\n", "response.incomplete"},
		{"messages_error", "event: error\ndata: {\"type\":\"error\",\"error\":{\"type\":\"overloaded_error\"}}\n\n", "overloaded_error"},
		{"oversized", "data: " + strings.Repeat("x", (1<<20)+1) + "\n\n", "invalid_stream"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			err := consumeSSE(strings.NewReader(tc.body), func(string, string, string) error { return nil })
			if tc.code == "" {
				if err != nil {
					t.Fatal(err)
				}
				return
			}
			var remote *APIError
			if !errors.As(err, &remote) || remote.Code != tc.code {
				t.Fatalf("error=%v want=%s", err, tc.code)
			}
		})
	}
}
func TestRawStreamingFlushesAndPreservesEvents(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		fmt.Fprint(w, "event: response.output_text.delta\ndata: {\"type\":\"response.output_text.delta\",\"delta\":\"hello\"}\n\nevent: response.completed\ndata: {\"type\":\"response.completed\"}\n\n")
	}))
	defer server.Close()
	client := NewClient(ClientOptions{BaseURL: server.URL, HTTPClient: server.Client()})
	out := &flushWriter{}
	if err := client.Request(context.Background(), "POST", "/responses", map[string]any{"stream": true}, true, out); err != nil {
		t.Fatal(err)
	}
	if out.flushes != 2 || !strings.Contains(out.String(), "event: response.output_text.delta") || !strings.Contains(out.String(), "hello") {
		t.Fatalf("flushes=%d out=%s", out.flushes, out.String())
	}
}

type flushWriter struct {
	bytes.Buffer
	flushes int
}

func (w *flushWriter) Flush() error { w.flushes++; return nil }
func TestRequestAndStreamingDeadlinesCancelServer(t *testing.T) {
	for _, stream := range []bool{false, true} {
		t.Run(fmt.Sprint(stream), func(t *testing.T) {
			canceled := make(chan struct{})
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if stream {
					w.Header().Set("Content-Type", "text/event-stream")
					fmt.Fprint(w, "data: {\"choices\":[]}\n\n")
					w.(http.Flusher).Flush()
				}
				<-r.Context().Done()
				close(canceled)
			}))
			defer server.Close()
			client := NewClient(ClientOptions{BaseURL: server.URL, HTTPClient: server.Client()})
			ctx, cancel := context.WithTimeout(context.Background(), 50*time.Millisecond)
			defer cancel()
			var out bytes.Buffer
			err := client.Request(ctx, "POST", "/chat/completions", nil, stream, &out)
			if !errors.Is(err, context.DeadlineExceeded) {
				t.Fatalf("error=%v", err)
			}
			select {
			case <-canceled:
			case <-time.After(time.Second):
				t.Fatal("server request was not canceled")
			}
		})
	}
}
func TestNoRedirectOrSandboxFallback(t *testing.T) {
	calls := 0
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls++
		http.Redirect(w, r, "/v1/chat/completions", http.StatusTemporaryRedirect)
	}))
	defer server.Close()
	client := NewClient(ClientOptions{BaseURL: server.URL + "/v1", APIKey: "sk-aetherapi-test"})
	var out bytes.Buffer
	err := client.Request(context.Background(), "POST", "/sandbox/chat/completions", map[string]string{"model": "aether-sandbox"}, false, &out)
	var remote *APIError
	if !errors.As(err, &remote) || remote.StatusCode != 307 || calls != 1 || out.Len() != 0 {
		t.Fatalf("error=%v calls=%d out=%s", err, calls, &out)
	}
}
func TestSandboxRequiresCanonicalPathAndConfirmation(t *testing.T) {
	calls := 0
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { calls++; fmt.Fprint(w, `{"ok":true}`) }))
	defer server.Close()
	for _, base := range []string{server.URL + "/proxy", server.URL + "/v1"} {
		client := NewClient(ClientOptions{BaseURL: base, HTTPClient: server.Client()})
		var out bytes.Buffer
		err := client.Request(context.Background(), "POST", "/sandbox/chat/completions", nil, false, &out)
		var remote *APIError
		if !errors.As(err, &remote) || out.Len() != 0 {
			t.Fatalf("error=%v out=%s", err, &out)
		}
	}
	if calls != 1 {
		t.Fatalf("noncanonical sandbox path was called: calls=%d", calls)
	}
	if allowedPath("POST", "/sandbox/embeddings") || allowedPath("GET", "/usage?period=30&other=bad") {
		t.Fatal("unsupported endpoint accepted")
	}
}
func TestStreamChatReportsInBandErrorAfterPartialText(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		fmt.Fprint(w, "data: {\"choices\":[{\"delta\":{\"content\":\"partial\"}}]}\n\ndata: {\"error\":{\"code\":\"upstream_failed\"}}\n\n")
	}))
	defer server.Close()
	client := NewClient(ClientOptions{BaseURL: server.URL, HTTPClient: server.Client()})
	var got strings.Builder
	err := client.StreamChat(context.Background(), ChatRequest{Model: "test", Prompt: "hi"}, func(s string) error { got.WriteString(s); return nil })
	var remote *APIError
	if !errors.As(err, &remote) || remote.Code != "upstream_failed" || got.String() != "partial" {
		t.Fatalf("err=%v output=%s", err, got.String())
	}
}
func TestCatalogRoundTripsFutureMetadata(t *testing.T) {
	raw := `{"id":"test","endpoint":"/v1/chat/completions","capabilities":{"tools":true,"unknown":null,"future":["x"]},"input_cost":0.25,"context_pricing":{"tiers":[1,2]},"new_optional":{"x":1}}`
	var model Model
	if err := json.Unmarshal([]byte(raw), &model); err != nil {
		t.Fatal(err)
	}
	// An unversioned model-wide boolean is not the catalog contract.
	for _, feature := range []string{"tools", "unknown", "future", "vision"} {
		if state := model.Capability("chat/completions", feature); state != CapabilityUnknown {
			t.Fatalf("%s guessed as %s", feature, state)
		}
	}
	encoded, err := json.Marshal(model)
	if err != nil {
		t.Fatal(err)
	}
	var original, roundtrip map[string]any
	_ = json.Unmarshal([]byte(raw), &original)
	_ = json.Unmarshal(encoded, &roundtrip)
	if fmt.Sprint(original) != fmt.Sprint(roundtrip) {
		t.Fatalf("roundtrip=%s", encoded)
	}
}

func TestEndpointScopedCapabilities(t *testing.T) {
	raw := `{"id":"m","endpoint":"/v1/chat/completions","supported_endpoints":["/v1/chat/completions","/v1/responses"],"capabilities_version":1,"capabilities":{
		"/v1/chat/completions":{"tool_calling":"supported","parallel_tool_calls":"unknown","structured_outputs":{"json_schema":"unsupported"},"modalities":{"input":{"text":"supported","image":"unknown"}},"reasoning":{"supported":"supported","options":{}},"max_output_tokens":8192,"vision":true,"bad":"SUPPORTED","nested_null":null},
		"/v1/responses":{"tool_calling":"unsupported","structured_outputs":{"json_schema":"supported"}},
		"/v1/responses/compact":{"tool_calling":"unsupported"}}}`
	var model Model
	if err := json.Unmarshal([]byte(raw), &model); err != nil {
		t.Fatal(err)
	}
	for _, tc := range []struct {
		endpoint, feature string
		want              CapabilityState
	}{
		{"chat/completions", "tool_calling", CapabilitySupported},
		{"/v1/chat/completions", "tool_calling", CapabilitySupported},
		{"/chat/completions", "tool_calling", CapabilitySupported},
		{"v1/chat/completions/", "tool_calling", CapabilitySupported},
		{"responses", "tool_calling", CapabilityUnsupported},
		{"responses/compact", "tool_calling", CapabilityUnsupported},
		{"chat/completions", "parallel_tool_calls", CapabilityUnknown},
		{"chat/completions", "structured_outputs.json_schema", CapabilityUnsupported},
		{"responses", "structured_outputs.json_schema", CapabilitySupported},
		{"chat/completions", "modalities.input.text", CapabilitySupported},
		{"chat/completions", "modalities.input.image", CapabilityUnknown},
		{"chat/completions", "modalities.output.text", CapabilityUnknown},
		{"chat/completions", "reasoning.supported", CapabilitySupported},
		{"responses", "reasoning.supported", CapabilityUnknown},
		{"chat/completions", "missing_feature", CapabilityUnknown},
		{"chat/completions", "vision", CapabilityUnknown},
		{"chat/completions", "bad", CapabilityUnknown},
		{"chat/completions", "nested_null", CapabilityUnknown},
		{"chat/completions", "structured_outputs", CapabilityUnknown},
		{"chat/completions", "max_output_tokens", CapabilityUnknown},
		{"messages", "tool_calling", CapabilityUnknown}, // omitted endpoint: never reuse another endpoint
		{"", "tool_calling", CapabilityUnknown},
	} {
		if got := model.Capability(tc.endpoint, tc.feature); got != tc.want {
			t.Errorf("%s %s = %s want %s", tc.endpoint, tc.feature, got, tc.want)
		}
	}
	if !model.DeclaresEndpoint("responses/compact") || !model.DeclaresEndpoint("/responses") || model.DeclaresEndpoint("messages") {
		t.Fatal("endpoint declarations")
	}
	for _, version := range []string{``, `,"capabilities_version":2`, `,"capabilities_version":"1"`, `,"capabilities_version":null`} {
		var other Model
		_ = json.Unmarshal([]byte(`{"id":"v","capabilities":{"/v1/chat/completions":{"tool_calling":"supported"}}`+version+`}`), &other)
		if got := other.Capability("chat/completions", "tool_calling"); got != CapabilityUnknown {
			t.Errorf("version %q interpreted as %s", version, got)
		}
	}
	var conflict Model
	_ = json.Unmarshal([]byte(`{"id":"c","capabilities_version":1,"capabilities":{"/v1/chat/completions":{"tool_calling":"supported"},"chat/completions":{"tool_calling":"unsupported"}}}`), &conflict)
	if got := conflict.Capability("chat/completions", "tool_calling"); got != CapabilityUnknown {
		t.Errorf("conflicting endpoint aliases = %s", got)
	}
}

func TestCapabilityPathValidation(t *testing.T) {
	for _, ok := range []string{"tool_calling", "parallel_tool_calls", "structured_outputs.json_schema", "modalities.input.image", "modalities.output.embedding", "reasoning.supported", "future_feature"} {
		if err := ValidateCapabilityPath(ok); err != nil {
			t.Errorf("%s: %v", ok, err)
		}
	}
	for _, bad := range []string{"", "Tool_Calling", "a..b", ".x", "tool calling", "structured_outputs", "modalities", "modalities.input", "reasoning", "reasoning.options", "reasoning.options.reasoning_effort", "max_output_tokens", "output_token_limit", "access", "access.allowed"} {
		if ValidateCapabilityPath(bad) == nil {
			t.Errorf("%q accepted", bad)
		}
	}
}

func TestNonStreamingResponsesTerminalStatus(t *testing.T) {
	for _, tc := range []struct {
		name, path, body, code, status string
	}{
		{"failed", "/responses", `{"id":"resp_fixture","object":"response","created_at":0,"model":"fixture","status":"failed","output":[],"error":{"code":"server_error","message":"Synthetic response failure"}}`, "server_error", "failed"},
		{"failed_type_only", "/responses", `{"object":"response","status":"failed","error":{"type":"provider_error"}}`, "provider_error", "failed"},
		{"failed_without_error", "/responses", `{"object":"response","status":"failed","error":null}`, "response.failed", "failed"},
		{"incomplete", "/responses", `{"id":"r","object":"response","status":"incomplete","incomplete_details":{"reason":"max_output_tokens"},"output":[]}`, "response.incomplete", "incomplete"},
		{"cancelled", "/responses", `{"object":"response","status":"cancelled"}`, "response.cancelled", "cancelled"},
		{"sandbox_failed", "/sandbox/responses", `{"object":"response","status":"failed","error":{"code":"sandbox_failure"}}`, "sandbox_failure", "failed"},
		{"completed", "/responses", `{"object":"response","status":"completed","output":[]}`, "", ""},
		{"background_queued", "/responses", `{"object":"response","status":"queued"}`, "", ""},
		{"background_in_progress", "/responses", `{"object":"response","status":"in_progress"}`, "", ""},
		{"other_object", "/responses", `{"object":"list","status":"failed"}`, "", ""},
		{"array", "/responses", `[{"status":"failed"}]`, "", ""},
		{"chat_untouched", "/chat/completions", `{"object":"chat.completion","status":"failed","error":{"code":"x"},"choices":[]}`, "", ""},
		{"messages_untouched", "/messages", `{"type":"message","status":"failed","content":[]}`, "", ""},
	} {
		t.Run(tc.name, func(t *testing.T) {
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if strings.HasPrefix(tc.path, "/sandbox/") {
					w.Header().Set("X-Aether-Sandbox", "true")
				}
				w.Header().Set("X-Request-Id", "req-fixture")
				fmt.Fprint(w, tc.body)
			}))
			defer server.Close()
			client := NewClient(ClientOptions{BaseURL: server.URL + "/v1", HTTPClient: server.Client()})
			var out bytes.Buffer
			err := client.Request(context.Background(), "POST", tc.path, map[string]any{}, false, &out)
			if tc.code == "" {
				if err != nil || !json.Valid(out.Bytes()) {
					t.Fatalf("err=%v out=%s", err, out.String())
				}
				return
			}
			var remote *APIError
			if !errors.As(err, &remote) || remote.Code != tc.code || remote.ResponseStatus != tc.status || remote.RequestID != "req-fixture" || remote.StatusCode != 0 || out.Len() != 0 {
				t.Fatalf("err=%#v out=%s", err, out.String())
			}
		})
	}
}
