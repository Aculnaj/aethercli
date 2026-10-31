package commands

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/Aculnaj/aethercli/internal/api"
	"github.com/Aculnaj/aethercli/internal/secrets"
	"github.com/Aculnaj/aethercli/internal/update"
)

type guardedStore struct {
	gets, sets, deletes int
	key                 string
	err                 error
}

func (s *guardedStore) Get() (string, error) {
	s.gets++
	if s.err != nil {
		return "", s.err
	}
	if s.key == "" {
		return "", secrets.ErrNotFound
	}
	return s.key, nil
}
func (s *guardedStore) Set(v string) error { s.sets++; s.key = v; return nil }
func (s *guardedStore) Delete() error      { s.deletes++; s.key = ""; return nil }

func commandDeps(t *testing.T) (Deps, *bytes.Buffer, *bytes.Buffer) {
	t.Helper()
	t.Setenv(secrets.EnvAPIKey, "")
	out, errOut := &bytes.Buffer{}, &bytes.Buffer{}
	return Deps{ConfigPath: filepath.Join(t.TempDir(), "config.json"), Secrets: &guardedStore{key: "sk-aetherapi-test"}, In: strings.NewReader(""), Out: out, Err: errOut, StdinHasData: func() bool { return false }}, out, errOut
}
func runCommand(deps Deps, args ...string) int { return Execute(context.Background(), deps, args) }
func errorEnvelope(t *testing.T, buffer *bytes.Buffer) *CLIError {
	t.Helper()
	var envelope struct {
		Error *CLIError `json:"error"`
	}
	if err := json.Unmarshal(buffer.Bytes(), &envelope); err != nil || envelope.Error == nil {
		t.Fatalf("invalid error envelope: %s (%v)", buffer, err)
	}
	return envelope.Error
}
func testServerDeps(t *testing.T, handler http.HandlerFunc) (Deps, *bytes.Buffer, *bytes.Buffer) {
	t.Helper()
	deps, out, errOut := commandDeps(t)
	server := httptest.NewServer(handler)
	t.Cleanup(server.Close)
	deps.ConfigPath = writeConfig(t, fmt.Sprintf(`{"base_url":%q}`, server.URL+"/v1"))
	return deps, out, errOut
}

func TestHeadlessNeverPromptsOrWrites(t *testing.T) {
	for _, args := range [][]string{{"--json"}, {"setup", "--json"}, {"tui", "--json"}, {"chat", "--json"}, {"ask", "hello", "--model", "test", "--json"}} {
		t.Run(strings.Join(args, "_"), func(t *testing.T) {
			deps, out, errOut := commandDeps(t)
			store := &guardedStore{}
			deps.Secrets = store
			deps.In = strings.NewReader("sk-aetherapi-must-not-be-consumed\nmodel\nprompt\n")
			if code := runCommand(deps, args...); code != ExitUsage && code != ExitAuth {
				t.Fatalf("exit=%d stderr=%s", code, errOut)
			}
			if out.Len() != 0 || strings.Contains(errOut.String(), "AetherAPI key:") || store.sets != 0 || store.deletes != 0 {
				t.Fatal("headless command prompted or mutated state")
			}
			if _, err := os.Stat(deps.ConfigPath); !os.IsNotExist(err) {
				t.Fatalf("created config: %v", err)
			}
			_ = errorEnvelope(t, errOut)
		})
	}
}
func TestExplicitModelNeedsNoSavedDefault(t *testing.T) {
	deps, out, errOut := commandDeps(t)
	client := &fakeAPIClient{chatContent: "answer"}
	deps.ClientFactory = func(string, string) APIClient { return client }
	if code := runCommand(deps, "ask", "hello", "--model", "catalog-id", "--json"); code != 0 {
		t.Fatalf("exit=%d %s", code, errOut)
	}
	if !json.Valid(out.Bytes()) || client.chatRequest.Model != "catalog-id" || errOut.Len() != 0 {
		t.Fatalf("stdout=%s stderr=%s", out, errOut)
	}
}
func TestMissingAndEmptyPromptExitUsage(t *testing.T) {
	for _, args := range [][]string{{"ask", "--model", "test", "--json"}, {"ask", " ", "--model", "test", "--json"}} {
		deps, _, errOut := commandDeps(t)
		if code := runCommand(deps, args...); code != ExitUsage {
			t.Fatalf("exit=%d %s", code, errOut)
		}
	}
}
func TestPipedAskAndChatJSON(t *testing.T) {
	for _, name := range []string{"ask", "chat"} {
		t.Run(name, func(t *testing.T) {
			deps, out, errOut := commandDeps(t)
			deps.In = strings.NewReader("piped prompt\n")
			deps.StdinHasData = func() bool { return true }
			client := &fakeAPIClient{chatContent: "answer"}
			deps.ClientFactory = func(string, string) APIClient { return client }
			if code := runCommand(deps, name, "--model", "catalog-id", "--json"); code != 0 {
				t.Fatalf("exit=%d %s", code, errOut)
			}
			if !json.Valid(out.Bytes()) || client.chatRequest.Prompt != "piped prompt" || errOut.Len() != 0 {
				t.Fatalf("stdout=%s stderr=%s", out, errOut)
			}
		})
	}
}
func TestAutomationSkipsUpdatesAndSpinner(t *testing.T) {
	for _, flags := range [][]string{{}, {"--json"}, {"--non-interactive"}} {
		deps, _, errOut := commandDeps(t)
		deps.CurrentVersion = "v1.0.0"
		checker := &fakeUpdateChecker{release: update.Release{Version: "v2.0.0"}}
		deps.UpdateChecker = checker
		deps.ClientFactory = func(string, string) APIClient { return &fakeAPIClient{chatContent: "answer"} }
		if len(flags) > 0 {
			deps.Interactive = func() bool { return true }
		}
		args := append([]string{"ask", "hello", "--model", "model"}, flags...)
		if code := runCommand(deps, args...); code != 0 || checker.calls != 0 || errOut.Len() != 0 {
			t.Fatalf("exit=%d updates=%d stderr=%s", code, checker.calls, errOut)
		}
		if _, err := os.Stat(deps.ConfigPath); !os.IsNotExist(err) {
			t.Fatalf("automation wrote config: %v", err)
		}
	}
}
func TestStableRemoteErrorsAndNoRetry(t *testing.T) {
	for _, tc := range []struct {
		status, exit int
		code         string
	}{{401, 3, "invalid_api_key"}, {403, 3, "permission_denied"}, {429, 4, "rate_limit_exceeded"}, {504, 5, "provider_timeout"}, {503, 6, "provider_unavailable"}, {400, 6, "invalid_request"}} {
		t.Run(tc.code, func(t *testing.T) {
			calls := 0
			deps, out, errOut := testServerDeps(t, func(w http.ResponseWriter, r *http.Request) {
				calls++
				w.Header().Set("Retry-After", "2")
				w.Header().Set("X-Request-Id", "req-test")
				w.WriteHeader(tc.status)
				fmt.Fprintf(w, `{"error":{"code":%q,"message":"test failure"}}`, tc.code)
			})
			if exit := runCommand(deps, "ask", "hello", "--model", "model", "--json"); exit != tc.exit {
				t.Fatalf("exit=%d stderr=%s", exit, errOut)
			}
			failure := errorEnvelope(t, errOut)
			if failure.Code != tc.code || failure.HTTPStatus != tc.status || failure.RetryAfter != "2" || failure.RequestID != "req-test" || calls != 1 || out.Len() != 0 {
				t.Fatalf("failure=%+v calls=%d stdout=%s", failure, calls, out)
			}
		})
	}
}
func TestMachineHelpAndParserErrors(t *testing.T) {
	for _, args := range [][]string{{"--help", "--json"}, {"models", "show", "--help", "--json"}, {"help", "api", "--json"}, {"config", "set", "--help", "--json"}, {"--version", "--json"}} {
		deps, out, errOut := commandDeps(t)
		if code := runCommand(deps, args...); code != 0 || !json.Valid(out.Bytes()) || errOut.Len() != 0 {
			t.Fatalf("args=%v exit=%d out=%s err=%s", args, code, out, errOut)
		}
	}
	for _, args := range [][]string{{"no-such-command", "--json"}, {"models", "--bad-flag", "--json"}, {"config", "set", "--json"}, {"sandbox", "--delay-ms", "bad", "--json"}} {
		deps, _, errOut := commandDeps(t)
		if code := runCommand(deps, args...); code != 2 {
			t.Fatalf("args=%v exit=%d stderr=%s", args, code, errOut)
		}
		_ = errorEnvelope(t, errOut)
	}
}
func TestConfigPathPreviewAndSecretSafety(t *testing.T) {
	deps, out, errOut := commandDeps(t)
	alternate := filepath.Join(t.TempDir(), "alternate.json")
	if code := runCommand(deps, "--config", alternate, "config", "set", "default-model", "new-model", "--dry-run", "--json"); code != 0 || !json.Valid(out.Bytes()) {
		t.Fatalf("exit=%d %s", code, errOut)
	}
	if _, err := os.Stat(alternate); !os.IsNotExist(err) {
		t.Fatal("dry-run wrote file")
	}
	out.Reset()
	if code := runCommand(deps, "--config", alternate, "config", "set", "default-model", "new-model", "--json"); code != 0 {
		t.Fatalf("exit=%d %s", code, errOut)
	}
	data, err := os.ReadFile(alternate)
	if err != nil || !bytes.Contains(data, []byte("new-model")) {
		t.Fatalf("config=%s %v", data, err)
	}
	out.Reset()
	errOut.Reset()
	if code := runCommand(deps, "config", "set", "default-model", "sk-aetherapi-DO-NOT-PRINT", "--json"); code != 2 {
		t.Fatalf("exit=%d", code)
	}
	if strings.Contains(out.String()+errOut.String(), "DO-NOT-PRINT") {
		t.Fatal("secret exposed")
	}
	out.Reset()
	errOut.Reset()
	if code := runCommand(deps, "config", "clear", "key", "--dry-run", "--json"); code != 0 {
		t.Fatalf("exit=%d %s", code, errOut)
	}
	if deps.Secrets.(*guardedStore).deletes != 0 {
		t.Fatal("dry-run deleted credential")
	}
}
func TestEnvKeyBypassesKeychainAndConnectRejected(t *testing.T) {
	deps, _, errOut := commandDeps(t)
	store := &guardedStore{err: fmt.Errorf("keychain locked")}
	deps.Secrets = store
	t.Setenv(secrets.EnvAPIKey, "sk-aetherapi-environment")
	deps.ClientFactory = func(_ string, key string) APIClient {
		if key != "sk-aetherapi-environment" {
			t.Error("wrong auth source")
		}
		return &fakeAPIClient{chatContent: "ok"}
	}
	if exit := runCommand(deps, "ask", "hello", "--model", "test", "--json"); exit != 0 || store.gets != 0 {
		t.Fatalf("exit=%d gets=%d %s", exit, store.gets, errOut)
	}
	t.Setenv(secrets.EnvAPIKey, "aeth_dev_do-not-use")
	errOut.Reset()
	if exit := runCommand(deps, "ask", "hello", "--model", "test", "--json"); exit != ExitAuth || strings.Contains(errOut.String(), "do-not-use") {
		t.Fatalf("exit=%d %s", exit, errOut)
	}
}
func TestDoctorReadOnlyAndOffline(t *testing.T) {
	var paths []string
	deps, out, errOut := testServerDeps(t, func(w http.ResponseWriter, r *http.Request) {
		paths = append(paths, r.Method+" "+r.URL.Path)
		if r.Method != "GET" {
			t.Error("doctor made a write request")
		}
		w.Header().Set("Content-Type", "application/json")
		if r.URL.Path == "/v1/models" {
			if r.Header.Get("Authorization") != "" {
				t.Error("public reachability probe sent key")
			}
			fmt.Fprint(w, `{"data":[]}`)
		} else if r.URL.Path == "/v1/usage" {
			fmt.Fprint(w, `{"account":{"credits":12}}`)
		} else {
			t.Error("unexpected route")
		}
	})
	before, _ := os.ReadFile(deps.ConfigPath)
	if code := runCommand(deps, "doctor", "--json"); code != 0 {
		t.Fatalf("exit=%d %s", code, errOut)
	}
	var report diagnosticReport
	if json.Unmarshal(out.Bytes(), &report) != nil || !report.OK || len(paths) != 2 || strings.Contains(out.String(), "sk-aetherapi-") {
		t.Fatalf("report=%s paths=%v", out, paths)
	}
	after, _ := os.ReadFile(deps.ConfigPath)
	if !bytes.Equal(before, after) {
		t.Fatal("doctor modified config")
	}
	out.Reset()
	if code := runCommand(deps, "status", "--offline", "--json"); code != 0 || len(paths) != 2 {
		t.Fatalf("offline exit=%d requests=%v", code, paths)
	}
	if store := deps.Secrets.(*guardedStore); store.sets != 0 || store.deletes != 0 {
		t.Fatal("diagnostic changed keychain")
	}
}
func TestDoctorInvalidConfigDoesNotUseDefaultBackend(t *testing.T) {
	deps, out, errOut := commandDeps(t)
	deps.ConfigPath = writeConfig(t, `{"base_url":"http://remote.invalid/v1"}`)
	deps.ClientFactory = func(string, string) APIClient { t.Fatal("invalid config triggered network"); return nil }
	if exit := runCommand(deps, "doctor", "--json"); exit != 7 || !json.Valid(out.Bytes()) || errorEnvelope(t, errOut).Code != "diagnostic_failed" {
		t.Fatalf("exit=%d out=%s err=%s", exit, out, errOut)
	}
}
func TestUsageAndBudgetUseActualEndpoints(t *testing.T) {
	for _, name := range []string{"usage", "budget"} {
		for _, jsonOut := range []bool{true, false} {
			t.Run(fmt.Sprintf("%s_%v", name, jsonOut), func(t *testing.T) {
				deps, out, errOut := testServerDeps(t, func(w http.ResponseWriter, r *http.Request) {
					want := "/v1/budget-alerts"
					if name == "usage" {
						want = "/v1/usage"
						if r.URL.Query().Get("period") != "30" {
							t.Error("period not forwarded")
						}
					}
					if r.URL.Path != want || r.Method != "GET" {
						t.Errorf("route=%s %s", r.Method, r.URL)
					}
					fmt.Fprint(w, `{"account":{"used_tokens":123},"report":{"coverage":"partial"},"forecast":null}`)
				})
				args := []string{name}
				if jsonOut {
					args = append(args, "--json")
				}
				if exit := runCommand(deps, args...); exit != 0 {
					t.Fatalf("exit=%d %s", exit, errOut)
				}
				if jsonOut && !json.Valid(out.Bytes()) || !strings.Contains(out.String(), "123") || !strings.Contains(out.String(), "partial") {
					t.Fatalf("out=%s", out)
				}
			})
		}
	}
}
func TestRawRequestProtocolsAndInput(t *testing.T) {
	for _, protocol := range []string{"chat", "responses", "messages"} {
		for _, stdin := range []bool{true, false} {
			t.Run(fmt.Sprintf("%s_%v", protocol, stdin), func(t *testing.T) {
				deps, out, errOut := testServerDeps(t, func(w http.ResponseWriter, r *http.Request) {
					path, _ := protocolPath(protocol)
					if r.URL.Path != "/v1"+path || r.Method != "POST" || r.Header.Get("Authorization") != "Bearer sk-aetherapi-test" {
						t.Errorf("request=%s %s", r.Method, r.URL)
					}
					data, _ := io.ReadAll(r.Body)
					if !bytes.Contains(data, []byte(`"custom_future_field"`)) {
						t.Error("request fields lost")
					}
					fmt.Fprint(w, `{"result":"ok","usage":{"total_tokens":4}}`)
				})
				body := `{"model":"catalog-model","messages":[],"input":"hi","custom_future_field":{"enabled":true}}`
				file := "-"
				deps.In = strings.NewReader(body)
				if !stdin {
					file = filepath.Join(t.TempDir(), "request.json")
					if err := os.WriteFile(file, []byte(body), 0600); err != nil {
						t.Fatal(err)
					}
				}
				if exit := runCommand(deps, "api", "--protocol", protocol, "--request", file, "--json"); exit != 0 || !json.Valid(out.Bytes()) {
					t.Fatalf("exit=%d out=%s err=%s", exit, out, errOut)
				}
			})
		}
	}
}
func TestUnsafeOrMalformedRawRequestsNeverReachServer(t *testing.T) {
	for _, tc := range []struct {
		args []string
		body string
	}{{[]string{"--endpoint", "https://other.invalid/messages"}, `{}`}, {[]string{"--endpoint", "../chat/completions"}, `{}`}, {[]string{"--protocol", "chat"}, `[]`}, {[]string{"--protocol", "chat"}, `{} {}`}, {[]string{"--protocol", "chat"}, `{"stream":null}`}, {[]string{"--protocol", "chat"}, `{"stream":"true"}`}, {[]string{}, `{}`}, {[]string{"--protocol", "chat", "--endpoint", "responses"}, `{}`}} {
		deps, _, errOut := testServerDeps(t, func(http.ResponseWriter, *http.Request) { t.Error("invalid request sent to server") })
		deps.In = strings.NewReader(tc.body)
		args := append([]string{"api", "--json", "--request", "-"}, tc.args...)
		if code := runCommand(deps, args...); code != 2 {
			t.Fatalf("args=%v exit=%d %s", args, code, errOut)
		}
	}
}
func TestSandboxScenariosNeverUseProductionRoutes(t *testing.T) {
	for _, scenario := range []string{"success", "rate_limit", "timeout", "provider_unavailable", "stream_error"} {
		for _, protocol := range []string{"chat", "responses", "messages"} {
			t.Run(scenario+"_"+protocol, func(t *testing.T) {
				calls := 0
				deps, out, errOut := testServerDeps(t, func(w http.ResponseWriter, r *http.Request) {
					calls++
					path, _ := protocolPath(protocol)
					if r.URL.Path != "/v1/sandbox"+path || r.Method != "POST" {
						t.Errorf("unsafe sandbox route: %s", r.URL)
					}
					var body map[string]any
					if json.NewDecoder(r.Body).Decode(&body) != nil || body["model"] != "aether-sandbox" {
						t.Error("invalid sandbox payload")
					}
					if body["sandbox"].(map[string]any)["scenario"] != scenario {
						t.Error("scenario not forwarded")
					}
					w.Header().Set("X-Aether-Sandbox", "true")
					switch scenario {
					case "rate_limit":
						w.WriteHeader(429)
						fmt.Fprint(w, `{"error":{"code":"sandbox_simulated_429"}}`)
					case "timeout":
						w.WriteHeader(504)
						fmt.Fprint(w, `{"error":{"code":"sandbox_simulated_timeout"}}`)
					case "provider_unavailable":
						w.WriteHeader(503)
						fmt.Fprint(w, `{"error":{"code":"sandbox_simulated_provider_unavailable"}}`)
					case "stream_error":
						w.Header().Set("Content-Type", "text/event-stream")
						fmt.Fprint(w, "data: {\"choices\":[{\"delta\":{\"content\":\"partial\"}}]}\n\ndata: {\"error\":{\"code\":\"sandbox_simulated_stream_error\"}}\n\n")
					default:
						fmt.Fprint(w, `{"usage":{"total_tokens":0}}`)
					}
				})
				args := []string{"sandbox", "--scenario", scenario, "--protocol", protocol, "--json"}
				want := 0
				switch scenario {
				case "rate_limit":
					want = 4
				case "timeout":
					want = 5
				case "provider_unavailable", "stream_error":
					want = 6
				}
				if scenario == "stream_error" {
					args = append(args, "--stream")
				}
				if code := runCommand(deps, args...); code != want || calls != 1 {
					t.Fatalf("exit=%d want=%d calls=%d out=%s err=%s", code, want, calls, out, errOut)
				}
				if want != 0 {
					_ = errorEnvelope(t, errOut)
				}
			})
		}
	}
}
func TestSandboxDisabledDoesNotFallback(t *testing.T) {
	calls := 0
	deps, out, errOut := testServerDeps(t, func(w http.ResponseWriter, r *http.Request) {
		calls++
		if !strings.HasPrefix(r.URL.Path, "/v1/sandbox/") {
			t.Fatal("production fallback")
		}
		w.WriteHeader(503)
		fmt.Fprint(w, `{"error":{"code":"sandbox_disabled","message":"Enable AETHER_SANDBOX_ENABLED"}}`)
	})
	if exit := runCommand(deps, "sandbox", "--json"); exit != 6 || calls != 1 || out.Len() != 0 || errorEnvelope(t, errOut).Code != "sandbox_disabled" {
		t.Fatalf("exit=%d calls=%d out=%s err=%s", exit, calls, out, errOut)
	}
}
func TestCatalogOptionalMetadataAndFilters(t *testing.T) {
	deps, out, errOut := testServerDeps(t, func(w http.ResponseWriter, r *http.Request) {
		fmt.Fprint(w, `{"data":[
			{"id":"chat-yes","owned_by":"Provider","endpoint":"/v1/chat/completions","supported_endpoints":["/v1/chat/completions","/v1/responses"],"input_cost":2.5,"capabilities_version":1,"capabilities":{"/v1/chat/completions":{"tool_calling":"supported","structured_outputs":{"json_schema":"supported"},"modalities":{"input":{"text":"supported","image":"unknown"}}},"/v1/responses":{"tool_calling":"unsupported"},"/v1/responses/compact":{"tool_calling":"unsupported"}}},
			{"id":"chat-unsupported","endpoint":"/v1/chat/completions","capabilities_version":1,"capabilities":{"/v1/chat/completions":{"tool_calling":"unsupported","structured_outputs":{"json_schema":"supported"}}}},
			{"id":"chat-unknown","endpoint":"/v1/chat/completions","capabilities_version":1,"capabilities":{"/v1/chat/completions":{"tool_calling":"unknown"}}},
			{"id":"chat-legacy-true","endpoint":"/v1/chat/completions","capabilities":{"tool_calling":true,"tools":true}},
			{"id":"chat-missing","endpoint":"/v1/chat/completions"},
			{"id":"image","endpoint":"/v1/images/generations"}]}`)
	})
	for _, tc := range []struct {
		args []string
		ids  string
	}{
		{[]string{"models"}, "chat-yes,chat-unsupported,chat-unknown,chat-legacy-true,chat-missing"},
		{[]string{"models", "list", "--all"}, "chat-yes,chat-unsupported,chat-unknown,chat-legacy-true,chat-missing,image"},
		{[]string{"models", "list", "--endpoint", "chat/completions", "--capability", "tool_calling"}, "chat-yes"},
		{[]string{"models", "list", "--endpoint", "/v1/chat/completions", "--capability", "tool_calling"}, "chat-yes"},
		{[]string{"models", "list", "--endpoint", "/chat/completions", "--capability", "tool_calling"}, "chat-yes"},
		{[]string{"models", "list", "--endpoint", "chat/completions", "--capability", "structured_outputs.json_schema"}, "chat-yes,chat-unsupported"},
		{[]string{"models", "list", "--endpoint", "chat/completions", "--capability", "tool_calling", "--capability", "structured_outputs.json_schema"}, "chat-yes"},
		{[]string{"models", "list", "--endpoint", "chat/completions", "--capability", "modalities.input.text"}, "chat-yes"},
		{[]string{"models", "list", "--endpoint", "chat/completions", "--capability", "modalities.input.image"}, ""},
		{[]string{"models", "list", "--endpoint", "chat/completions", "--capability", "tools"}, ""},
		{[]string{"models", "list", "--endpoint", "responses", "--capability", "tool_calling"}, ""},
		{[]string{"models", "list", "--endpoint", "messages", "--capability", "tool_calling"}, ""},
		{[]string{"models", "list", "--endpoint", "responses"}, "chat-yes"},
		{[]string{"models", "list", "--endpoint", "responses/compact"}, "chat-yes"},
		{[]string{"models", "list", "--provider", "provider"}, "chat-yes"},
		{[]string{"models", "list", "--endpoint", "images/generations"}, "image"},
		{[]string{"models", "list", "--search", "unknown"}, "chat-unknown"},
	} {
		out.Reset()
		errOut.Reset()
		args := append(tc.args, "--json")
		if exit := runCommand(deps, args...); exit != 0 {
			t.Fatalf("args=%v exit=%d %s", args, exit, errOut)
		}
		var models []api.Model
		if json.Unmarshal(out.Bytes(), &models) != nil {
			t.Fatalf("args=%v out=%s", args, out)
		}
		ids := make([]string, 0, len(models))
		for _, m := range models {
			ids = append(ids, m.ID)
		}
		if strings.Join(ids, ",") != tc.ids {
			t.Fatalf("args=%v ids=%v want %s", args, ids, tc.ids)
		}
	}
	for _, args := range [][]string{
		{"models", "list", "--capability", "tool_calling"},
		{"models", "list", "--all", "--capability", "tool_calling"},
		{"models", "list", "--endpoint", "chat/completions", "--capability", "structured_outputs"},
		{"models", "list", "--endpoint", "chat/completions", "--capability", "max_output_tokens"},
		{"models", "list", "--endpoint", "chat/completions", "--capability", "access.allowed"},
		{"models", "list", "--endpoint", "https://example.invalid/v1/chat/completions"},
	} {
		out.Reset()
		errOut.Reset()
		if exit := runCommand(deps, append(args, "--json")...); exit != ExitUsage || out.Len() != 0 || errorEnvelope(t, errOut).Code != "invalid_arguments" {
			t.Fatalf("args=%v exit=%d out=%s err=%s", args, exit, out, errOut)
		}
	}
	out.Reset()
	if exit := runCommand(deps, "models", "show", "chat-yes", "--json"); exit != 0 || !strings.Contains(out.String(), `"input_cost":2.5`) { // decoder formatting is immaterial
		var m map[string]any
		if json.Unmarshal(out.Bytes(), &m) != nil || m["input_cost"] != 2.5 {
			t.Fatalf("exit=%d out=%s err=%s", exit, out, errOut)
		}
	}
}

func TestRawResponsesTerminalStatusExitsNonzero(t *testing.T) {
	for _, tc := range []struct {
		name, body, code, status string
		exit                     int
	}{
		{"failed", `{"id":"resp_fixture","object":"response","created_at":0,"model":"fixture","status":"failed","output":[],"error":{"code":"server_error","message":"Synthetic response failure"}}`, "server_error", "failed", ExitBackend},
		{"failed_rate_limit", `{"object":"response","status":"failed","error":{"code":"rate_limit_exceeded","message":"slow down"}}`, "rate_limit_exceeded", "failed", ExitRateLimit},
		{"incomplete", `{"object":"response","status":"incomplete","incomplete_details":{"reason":"max_output_tokens"}}`, "response.incomplete", "incomplete", ExitBackend},
		{"cancelled", `{"object":"response","status":"cancelled"}`, "response.cancelled", "cancelled", ExitBackend},
		{"completed", `{"object":"response","status":"completed","output":[]}`, "", "", 0},
	} {
		t.Run(tc.name, func(t *testing.T) {
			deps, out, errOut := testServerDeps(t, func(w http.ResponseWriter, r *http.Request) {
				fmt.Fprint(w, tc.body)
			})
			deps.In = strings.NewReader(`{"model":"fixture","input":"synthetic"}`)
			exit := runCommand(deps, "api", "--protocol", "responses", "--request", "-", "--json")
			if exit != tc.exit {
				t.Fatalf("exit=%d out=%s err=%s", exit, out, errOut)
			}
			if tc.exit == 0 {
				if errOut.Len() != 0 || !json.Valid(out.Bytes()) {
					t.Fatalf("out=%s err=%s", out, errOut)
				}
				return
			}
			failure := errorEnvelope(t, errOut)
			if out.Len() != 0 || failure.Code != tc.code || failure.ResponseStatus != tc.status || failure.HTTPStatus != 0 || failure.ExitCode != tc.exit || strings.Contains(errOut.String(), "sk-aetherapi-test") {
				t.Fatalf("failure=%+v out=%s err=%s", failure, out, errOut)
			}
		})
	}
}

func TestEstimatesStayLabeledAndUseOutputPrice(t *testing.T) {
	deps, out, errOut := commandDeps(t)
	client := &fakeAPIClient{models: []api.Model{{ID: "priced", Endpoint: "/v1/chat/completions", OurPrice: "$2.50/1M in, $10.00/1M out"}}}
	deps.ClientFactory = func(string, string) APIClient { return client }
	if code := runCommand(deps, "ask", "12345678", "--model", "priced", "--estimate", "--max-tokens", "1000", "--json"); code != 0 {
		t.Fatalf("exit=%d %s", code, errOut)
	}
	var value struct {
		Estimated bool     `json:"estimated"`
		Cost      *float64 `json:"estimated_cost_usd"`
	}
	if json.Unmarshal(out.Bytes(), &value) != nil || !value.Estimated || value.Cost == nil || *value.Cost != 0.010005 || client.chatCalls != 0 {
		t.Fatalf("incorrect estimate or provider request: %s calls=%d", out, client.chatCalls)
	}
	out.Reset()
	if code := runCommand(deps, "ask", "hi", "--model", "priced", "--estimate", "--json"); code != 0 {
		t.Fatalf("exit=%d %s", code, errOut)
	}
	if json.Unmarshal(out.Bytes(), &value) != nil || value.Cost != nil {
		t.Fatalf("unknown output estimate must be null: %s", out)
	}
}
func TestPublicModelsNeverReadKeychain(t *testing.T) {
	deps, _, errOut := commandDeps(t)
	store := &guardedStore{err: fmt.Errorf("locked")}
	deps.Secrets = store
	deps.ClientFactory = func(_ string, key string) APIClient {
		if key != "" {
			t.Error("catalog sent credential")
		}
		return &fakeAPIClient{models: []api.Model{{ID: "test"}}}
	}
	for _, args := range [][]string{{"models", "--json"}, {"models", "show", "test", "--json"}} {
		if code := runCommand(deps, args...); code != 0 || store.gets != 0 {
			t.Fatalf("exit=%d gets=%d %s", code, store.gets, errOut)
		}
	}
}
func TestDocumentedRequestTemplates(t *testing.T) {
	for _, protocol := range []string{"chat", "responses", "messages"} {
		t.Run(protocol, func(t *testing.T) {
			deps, out, errOut := testServerDeps(t, func(w http.ResponseWriter, r *http.Request) {
				var body map[string]json.RawMessage
				if json.NewDecoder(r.Body).Decode(&body) != nil {
					t.Error("invalid request")
				}
				if string(body["model"]) != `"fixture-model"` {
					t.Error("model substitution failed")
				}
				field := "messages"
				if protocol == "responses" {
					field = "input"
				}
				if body[field] == nil {
					t.Errorf("missing protocol field %s", field)
				}
				fmt.Fprint(w, `{"result":"fixture success"}`)
			})
			data, err := os.ReadFile(filepath.Join("..", "..", "examples", protocol+".json"))
			if err != nil {
				t.Fatal(err)
			}
			deps.In = strings.NewReader(strings.ReplaceAll(string(data), "MODEL_ID", "fixture-model"))
			if code := runCommand(deps, "api", "--protocol", protocol, "--request", "-", "--json"); code != 0 || !json.Valid(out.Bytes()) {
				t.Fatalf("exit=%d out=%s err=%s", code, out, errOut)
			}
		})
	}
}
func TestConfigCanRepairInvalidBaseURL(t *testing.T) {
	deps, _, errOut := commandDeps(t)
	deps.ConfigPath = writeConfig(t, `{"base_url":"http://untrusted.example/v1"}`)
	if code := runCommand(deps, "config", "set", "base-url", "https://api.aetherapi.dev/v1", "--json"); code != 0 {
		t.Fatalf("exit=%d %s", code, errOut)
	}
}
func TestRawStreamRequestBooleanAndFlagOverride(t *testing.T) {
	for _, override := range []bool{false, true} {
		t.Run(fmt.Sprint(override), func(t *testing.T) {
			deps, out, errOut := testServerDeps(t, func(w http.ResponseWriter, r *http.Request) {
				var body struct {
					Stream bool `json:"stream"`
				}
				if json.NewDecoder(r.Body).Decode(&body) != nil {
					t.Fatal("invalid request")
				}
				if body.Stream == override {
					t.Error("stream setting ignored")
				}
				if body.Stream {
					w.Header().Set("Content-Type", "text/event-stream")
					fmt.Fprint(w, "data: [DONE]\n\n")
				} else {
					fmt.Fprint(w, `{"ok":true}`)
				}
			})
			deps.In = strings.NewReader(`{"model":"fixture","stream":true}`)
			args := []string{"api", "--protocol", "chat", "--request", "-", "--json"}
			if override {
				args = append(args, "--stream=false")
			}
			if code := runCommand(deps, args...); code != 0 {
				t.Fatalf("exit=%d out=%s err=%s", code, out, errOut)
			}
		})
	}
}
func TestCanceledOperationExitCode(t *testing.T) {
	deps, _, errOut := testServerDeps(t, func(http.ResponseWriter, *http.Request) { t.Error("canceled request reached server") })
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	deps.ClientFactory = func(base, key string) APIClient { return api.NewClient(api.ClientOptions{BaseURL: base, APIKey: key}) }
	if code := Execute(ctx, deps, []string{"ask", "hello", "--model", "fixture", "--json"}); code != 130 || errorEnvelope(t, errOut).Code != "canceled" {
		t.Fatalf("exit=%d stderr=%s", code, errOut)
	}
}

func TestRepairPreviewDoesNotExposeOldURLCredentials(t *testing.T) {
	deps, out, errOut := commandDeps(t)
	deps.ConfigPath = writeConfig(t, `{"base_url":"https://user:old-private-password@example.test/v1"}`)
	if code := runCommand(deps, "config", "set", "base-url", "https://example.test/v1", "--dry-run", "--json"); code != 0 {
		t.Fatalf("exit=%d %s", code, errOut)
	}
	if strings.Contains(out.String()+errOut.String(), "old-private-password") {
		t.Fatal("old URL credential leaked")
	}
}
func TestSandboxRejectsNonCanonicalBaseBeforeSending(t *testing.T) {
	deps, _, errOut := testServerDeps(t, func(http.ResponseWriter, *http.Request) { t.Error("noncanonical sandbox request sent") })
	data, _ := os.ReadFile(deps.ConfigPath)
	if err := os.WriteFile(deps.ConfigPath, bytes.ReplaceAll(data, []byte("/v1"), []byte("/proxy")), 0600); err != nil {
		t.Fatal(err)
	}
	if code := runCommand(deps, "sandbox", "--json"); code != 2 || errorEnvelope(t, errOut).Code != "sandbox_base_url" {
		t.Fatalf("exit=%d %s", code, errOut)
	}
}

func TestInvalidConfigErrorsAreConsistent(t *testing.T) {
	for _, args := range [][]string{{"config", "get"}, {"config", "validate"}, {"config", "set", "default-model", "fixture"}, {"config", "clear", "base-url"}} {
		deps, _, errOut := commandDeps(t)
		deps.ConfigPath = writeConfig(t, `{broken`)
		if code := runCommand(deps, append(args, "--json")...); code != ExitUsage || errorEnvelope(t, errOut).Code != "invalid_config" {
			t.Fatalf("args=%v exit=%d %s", args, code, errOut)
		}
	}
}
