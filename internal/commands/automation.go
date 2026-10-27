package commands

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/url"
	"os"
	"regexp"
	"strings"

	"github.com/mattn/go-isatty"
	"github.com/spf13/cobra"
	"github.com/spf13/pflag"

	"github.com/Aculnaj/aethercli/internal/api"
	"github.com/Aculnaj/aethercli/internal/config"
	"github.com/Aculnaj/aethercli/internal/prompt"
)

// Exit codes are a public automation contract. Error codes carry more detail.
const (
	ExitFailure    = 1
	ExitUsage      = 2
	ExitAuth       = 3
	ExitRateLimit  = 4
	ExitNetwork    = 5
	ExitBackend    = 6
	ExitDiagnostic = 7
)

type CLIError struct {
	Code       string `json:"code"`
	Message    string `json:"message"`
	ExitCode   int    `json:"exit_code"`
	HTTPStatus int    `json:"http_status,omitempty"`
	RetryAfter string `json:"retry_after,omitempty"`
	RequestID  string `json:"request_id,omitempty"`
	cause      error
}

func (e *CLIError) Error() string { return e.Message }
func (e *CLIError) Unwrap() error { return e.cause }
func usageError(message string) error {
	return &CLIError{Code: "invalid_arguments", Message: message, ExitCode: ExitUsage}
}
func authError(_ error) error {
	return &CLIError{Code: "credentials_unavailable", Message: "No valid AetherAPI key is available. Set AETHER_API_KEY or run `aethercli setup` in a terminal; check OS keychain access. Connect tokens are not supported.", ExitCode: ExitAuth}
}

var secretPattern = regexp.MustCompile(`(?:sk-aetherapi-|aeth_dev_)[^\s"'` + "`" + `<>]+`)

func redact(s string) string { return secretPattern.ReplaceAllString(s, "[REDACTED]") }

func classifyError(err error) *CLIError {
	var cli *CLIError
	if errors.As(err, &cli) {
		copy := *cli
		copy.Message = redact(copy.Message)
		return &copy
	}
	var remote *api.APIError
	if errors.As(err, &remote) {
		exit := ExitBackend
		switch {
		case remote.StatusCode == 401 || remote.StatusCode == 403 || remote.Code == "invalid_api_key":
			exit = ExitAuth
		case remote.StatusCode == 429 || remote.Code == "rate_limit_exceeded" || remote.Code == "rate_limit_error":
			exit = ExitRateLimit
		case remote.StatusCode == 408 || remote.StatusCode == 504:
			exit = ExitNetwork
		}
		code := remote.Code
		if code == "" {
			code = "api_error"
		}
		return &CLIError{Code: code, Message: redact(remote.UserMessage()), ExitCode: exit, HTTPStatus: remote.StatusCode, RetryAfter: remote.RetryAfter, RequestID: remote.RequestID}
	}
	if errors.Is(err, prompt.ErrEmptyPrompt) {
		return &CLIError{Code: "invalid_arguments", Message: "Prompt is empty; pass text as an argument or stdin", ExitCode: ExitUsage}
	}
	if errors.Is(err, context.Canceled) {
		return &CLIError{Code: "canceled", Message: "Operation canceled", ExitCode: 130}
	}
	var transport net.Error
	if errors.As(err, &transport) || errors.Is(err, context.DeadlineExceeded) {
		code := "network_error"
		if errors.Is(err, context.DeadlineExceeded) || transport != nil && transport.Timeout() {
			code = "timeout"
		}
		return &CLIError{Code: code, Message: "API connection failed; check base-url, connectivity and timeout", ExitCode: ExitNetwork}
	}
	// Cobra parsing errors occur before RunE. Keep their useful flag/command
	// guidance while removing credentials a caller may have pasted as an arg.
	message := redact(err.Error())
	for _, prefix := range []string{"unknown command ", "unknown flag:", "unknown shorthand flag:", "accepts ", "requires ", "required flag", "invalid argument ", "flag needs an argument:"} {
		if strings.HasPrefix(message, prefix) {
			return &CLIError{Code: "invalid_arguments", Message: message, ExitCode: ExitUsage}
		}
	}
	if errors.Is(err, os.ErrNotExist) {
		return &CLIError{Code: "file_not_found", Message: message, ExitCode: ExitFailure}
	}
	return &CLIError{Code: "operation_failed", Message: message, ExitCode: ExitFailure}
}

// Execute is the binary boundary: success data goes to stdout; a single error
// envelope goes to stderr. Libraries may continue to use NewRootCommand.
func Execute(ctx context.Context, deps Deps, args []string) int {
	deps = normalizeDeps(deps)
	root := NewRootCommand(deps)
	root.SetArgs(args)
	_, err := root.ExecuteContextC(ctx)
	if err == nil {
		return 0
	}
	failure := classifyError(err)
	jsonOut, _ := root.PersistentFlags().GetBool("json")
	// An unknown command may prevent Cobra from parsing the persistent flags.
	for _, arg := range args {
		if arg == "--" {
			break
		}
		if arg == "--json" || arg == "--json=true" {
			jsonOut = true
		}
		if arg == "--json=false" {
			jsonOut = false
		}
	}
	if jsonOut {
		_ = writeJSON(deps.Err, map[string]any{"error": failure})
	} else {
		_, _ = fmt.Fprintln(deps.Err, failure.Message)
	}
	return failure.ExitCode
}

func jsonRequested(cmd *cobra.Command) bool { value, _ := cmd.Flags().GetBool("json"); return value }
func writeJSON(out io.Writer, value any) error {
	e := json.NewEncoder(out)
	e.SetIndent("", "  ")
	return e.Encode(value)
}
func (deps Deps) path() string {
	if deps.configPath != nil {
		return deps.configPath()
	}
	return deps.ConfigPath
}
func terminalIO(in io.Reader, out io.Writer) bool {
	i, iok := in.(*os.File)
	o, ook := out.(*os.File)
	return iok && ook && isatty.IsTerminal(i.Fd()) && isatty.IsTerminal(o.Fd())
}
func promptAsker(deps Deps, reader *bufio.Reader) func(string) (string, error) {
	if deps.Interactive() {
		return lineAsker(deps.Err, reader)
	}
	return func(string) (string, error) {
		return "", usageError("missing prompt: pass an argument or pipe text to stdin")
	}
}
func thinkingSpinner(deps Deps) func() {
	if !deps.Interactive() {
		return func() {}
	}
	return startThinkingSpinner(deps.Err, thinkingSpinnerInterval)
}
func readConfig(deps Deps) (config.Config, error) {
	cfg, err := config.Load(deps.path())
	if err != nil {
		return cfg, &CLIError{Code: "invalid_config", Message: "Invalid aethercli config: " + err.Error(), ExitCode: ExitUsage, cause: err}
	}
	return cfg, nil
}

func loadConfig(deps Deps) (config.Config, error) {
	cfg, err := readConfig(deps)
	if err != nil {
		return cfg, err
	}
	if err == nil {
		err = config.Validate(cfg)
	}
	if err != nil {
		return cfg, &CLIError{Code: "invalid_config", Message: "Invalid aethercli config: " + err.Error(), ExitCode: ExitUsage, cause: err}
	}
	return cfg, nil
}
func validateInvocation(cmd *cobra.Command, args []string, deps Deps) error {
	if !deps.Interactive() && (cmd.Name() == "setup" || cmd.Name() == "tui" || cmd.Parent() == nil || cmd.Name() == "chat" && len(args) == 0 && !deps.StdinHasData()) {
		return usageError("interactive terminal required; use `ask` with a prompt and AETHER_API_KEY for automation")
	}
	if jsonRequested(cmd) && cmd.Name() == "ask" {
		stream, _ := cmd.Flags().GetBool("stream")
		if stream {
			return usageError("cannot combine ask --stream and --json; use api --stream for SSE")
		}
	}
	return nil
}

// Machine-readable help is generated from the live Cobra tree so flags and
// subcommands cannot silently drift from the executable.
func installMachineHelp(root *cobra.Command) {
	textHelp := root.HelpFunc()
	root.SetHelpFunc(func(cmd *cobra.Command, args []string) {
		if !jsonRequested(cmd) {
			textHelp(cmd, args)
			return
		}
		cmd.InitDefaultHelpFlag()
		cmd.InheritedFlags()
		type flagHelp struct {
			Name        string `json:"name"`
			Type        string `json:"type"`
			Default     string `json:"default"`
			Description string `json:"description"`
			Shorthand   string `json:"shorthand,omitempty"`
		}
		flags := make([]flagHelp, 0)
		seen := map[string]bool{}
		visit := func(f *pflag.Flag) {
			if !f.Hidden && !seen[f.Name] {
				flags = append(flags, flagHelp{f.Name, f.Value.Type(), f.DefValue, f.Usage, f.Shorthand})
				seen[f.Name] = true
			}
		}
		cmd.LocalFlags().VisitAll(visit)
		cmd.InheritedFlags().VisitAll(visit)
		children := make([]map[string]string, 0)
		for _, child := range cmd.Commands() {
			if !child.Hidden {
				children = append(children, map[string]string{"name": child.Name(), "summary": child.Short, "usage": child.UseLine()})
			}
		}
		_ = writeJSON(cmd.OutOrStdout(), map[string]any{"schema_version": 1, "name": cmd.CommandPath(), "usage": cmd.UseLine(), "summary": cmd.Short, "description": cmd.Long, "example": cmd.Example, "flags": flags, "commands": children, "exit_codes": map[int]string{0: "success", 1: "local failure", 2: "arguments/config", 3: "authentication/permission", 4: "rate limit", 5: "network/timeout", 6: "backend/protocol/stream", 7: "diagnostic failed", 130: "canceled"}})
	})
}

// Validate an explicit endpoint path without accepting URLs, query strings or
// path traversal. Protocol selection lives in api.go alongside the commands.
func validEndpoint(value string) bool {
	u, err := url.Parse(value)
	return err == nil && u.RawQuery == "" && u.Fragment == "" && u.Host == "" && !u.IsAbs() && !strings.Contains(value, "..")
}
