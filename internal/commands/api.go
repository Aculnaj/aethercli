package commands

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"strings"
	"time"

	"github.com/Aculnaj/aethercli/internal/api"
	"github.com/Aculnaj/aethercli/internal/secrets"
	"github.com/spf13/cobra"
)

type requestClient interface {
	Request(context.Context, string, string, any, bool, io.Writer) error
}

func rawClient(deps Deps) (requestClient, error) {
	cfg, err := loadConfig(deps)
	if err != nil {
		return nil, err
	}
	key, err := secrets.ResolveAPIKey(deps.Secrets)
	if err != nil {
		return nil, authError(err)
	}
	client, ok := deps.ClientFactory(cfg.BaseURL, key).(requestClient)
	if !ok {
		return nil, fmt.Errorf("API client does not support direct requests")
	}
	return client, nil
}

func protocolPath(protocol string) (string, error) {
	switch protocol {
	case "chat":
		return "/chat/completions", nil
	case "responses":
		return "/responses", nil
	case "messages":
		return "/messages", nil
	}
	return "", usageError("protocol must be chat, responses or messages")
}
func endpointPath(endpoint string) (string, error) {
	if !validEndpoint(endpoint) {
		return "", usageError("endpoint must be a supported path, never a URL")
	}
	path := "/" + strings.TrimPrefix(strings.TrimPrefix(endpoint, "/v1/"), "/")
	switch path {
	case "/chat/completions", "/responses", "/messages", "/embeddings", "/images/generations":
		return path, nil
	}
	return "", usageError("supported endpoints: chat/completions, responses, messages, embeddings, images/generations")
}
func newAPICommand(deps Deps) *cobra.Command {
	var file, endpoint, protocol string
	var stream bool
	var timeout time.Duration
	cmd := &cobra.Command{
		Use:     "api",
		Short:   "POST an explicit JSON request file or stdin to an API endpoint",
		Args:    cobra.NoArgs,
		Example: "  aethercli api --protocol chat --request request.json\n  cat request.json | aethercli api --endpoint responses --request - --stream",
		Long:    "Send a real API request using AETHER_API_KEY or the OS keychain. Select exactly one endpoint or protocol. Request fields are passed through; there are no retries. --stream forwards SSE and reports in-band errors on stderr.",
		RunE: func(cmd *cobra.Command, args []string) error {
			if (endpoint == "") == (protocol == "") {
				return usageError("choose exactly one of --endpoint or --protocol")
			}
			var path string
			var err error
			if protocol != "" {
				path, err = protocolPath(protocol)
			} else {
				path, err = endpointPath(endpoint)
			}
			if err != nil {
				return err
			}
			if timeout <= 0 {
				return usageError("timeout must be positive")
			}
			if file == "" {
				return usageError("pass --request FILE or --request - for stdin")
			}
			var input io.Reader = deps.In
			if file != "-" {
				f, err := os.Open(file)
				if err != nil {
					return err
				}
				defer f.Close()
				input = f
			}
			data, err := io.ReadAll(io.LimitReader(input, (32<<20)+1))
			if err != nil {
				return err
			}
			if len(data) > 32<<20 {
				return usageError("request exceeds 32 MiB")
			}
			var payload map[string]json.RawMessage
			if json.Unmarshal(data, &payload) != nil || payload == nil {
				return usageError("request must contain one JSON object")
			}
			if v, ok := payload["stream"]; ok {
				var requested bool
				if json.Unmarshal(v, &requested) != nil || string(v) == "null" {
					return usageError("request stream must be a boolean")
				}
				if !cmd.Flags().Changed("stream") {
					stream = requested
				}
			}
			if stream && (path == "/embeddings" || path == "/images/generations") {
				return usageError("streaming is supported only for chat, responses and messages")
			}
			if path != "/embeddings" && path != "/images/generations" {
				payload["stream"], _ = json.Marshal(stream)
			}
			client, err := rawClient(deps)
			if err != nil {
				return err
			}
			ctx, cancel := context.WithTimeout(cmd.Context(), timeout)
			defer cancel()
			return client.Request(ctx, http.MethodPost, path, payload, stream, deps.Out)
		},
	}
	cmd.Flags().StringVar(&file, "request", "", "JSON request file; - reads stdin")
	cmd.Flags().StringVar(&endpoint, "endpoint", "", "explicit endpoint: chat/completions, responses, messages, embeddings, images/generations")
	cmd.Flags().StringVar(&protocol, "protocol", "", "protocol: chat, responses, messages (alternative to --endpoint)")
	cmd.Flags().BoolVar(&stream, "stream", false, "request and forward SSE (also honored from request JSON)")
	cmd.Flags().DurationVar(&timeout, "timeout", 60*time.Second, "total request deadline, including stream body")
	return cmd
}

func newSandboxCommand(deps Deps) *cobra.Command {
	var protocol, scenario string
	var stream bool
	var delay int
	var timeout time.Duration
	cmd := &cobra.Command{
		Use:     "sandbox",
		Short:   "Run a simulated scenario exclusively through /v1/sandbox",
		Args:    cobra.NoArgs,
		Example: "  aethercli sandbox --scenario success --json\n  aethercli sandbox --scenario stream_error --stream --json",
		Long:    "Requires AETHER_SANDBOX_ENABLED on the server and a normal API key. Uses the fixed aether-sandbox model, never falls back to a production route. Simulated failures return normal nonzero error codes; --json --stream emits SSE on stdout and JSON errors on stderr.",
		RunE: func(cmd *cobra.Command, args []string) error {
			path, err := protocolPath(protocol)
			if err != nil {
				return err
			}
			switch scenario {
			case "success", "rate_limit", "timeout", "provider_unavailable", "stream_error":
			default:
				return usageError("unknown sandbox scenario")
			}
			if scenario == "stream_error" && !stream {
				return usageError("stream_error requires --stream")
			}
			if delay < 0 || delay > 5000 {
				return usageError("delay-ms must be between 0 and 5000")
			}
			if timeout <= 0 {
				return usageError("timeout must be positive")
			}
			payload := map[string]any{"model": "aether-sandbox", "stream": stream, "sandbox": map[string]any{"scenario": scenario, "delay_ms": delay}}
			if protocol == "responses" {
				payload["input"] = "Sandbox integration test"
			} else {
				payload["messages"] = []map[string]string{{"role": "user", "content": "Sandbox integration test"}}
				if protocol == "messages" {
					payload["max_tokens"] = 128
				}
			}
			cfg, err := loadConfig(deps)
			if err != nil {
				return err
			}
			base, _ := url.Parse(cfg.BaseURL)
			if base.Path != "/v1" {
				return &CLIError{Code: "sandbox_base_url", Message: "Sandbox requires base-url ending exactly in /v1; no request was sent", ExitCode: ExitUsage}
			}
			client, err := rawClient(deps)
			if err != nil {
				return err
			}
			ctx, cancel := context.WithTimeout(cmd.Context(), timeout)
			defer cancel()
			return client.Request(ctx, http.MethodPost, "/sandbox"+path, payload, stream, deps.Out)
		},
	}
	cmd.Flags().StringVar(&protocol, "protocol", "chat", "sandbox protocol: chat, responses, messages")
	cmd.Flags().StringVar(&scenario, "scenario", "success", "success, rate_limit, timeout, provider_unavailable, stream_error")
	cmd.Flags().BoolVar(&stream, "stream", false, "request and forward SSE")
	cmd.Flags().IntVar(&delay, "delay-ms", 250, "simulated server delay (0..5000 milliseconds)")
	cmd.Flags().DurationVar(&timeout, "timeout", 10*time.Second, "total request deadline")
	return cmd
}

// Referenced here to make the API client's streaming and error contract visible
// at the command boundary without extending the existing chat/TUI interface.
var _ requestClient = (*api.Client)(nil)
