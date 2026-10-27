package commands

import (
	"context"
	"fmt"
	"io"
	"net/http"
	"os"
	"strings"
	"time"

	"github.com/Aculnaj/aethercli/internal/secrets"
	"github.com/spf13/cobra"
)

type diagnosticCheck struct {
	Name   string `json:"name"`
	Status string `json:"status"`
	Detail string `json:"detail"`
	Hint   string `json:"hint,omitempty"`
}
type diagnosticReport struct {
	SchemaVersion    int               `json:"schema_version"`
	OK               bool              `json:"ok"`
	ConfigPath       string            `json:"config_path"`
	CredentialSource string            `json:"credential_source"`
	Checks           []diagnosticCheck `json:"checks"`
}

func newDiagnosticCommand(deps Deps, name string) *cobra.Command {
	var offline bool
	var timeout time.Duration
	cmd := &cobra.Command{
		Use:   name,
		Short: "Diagnose config, credentials and backend using read-only checks",
		Args:  cobra.NoArgs,
		Long:  "Report config, environment/keychain availability, public model-catalog reachability and API-key authentication via GET /usage. No config writes, provider requests, browser or update checks. --offline disables network. Keychain access is skipped when AETHER_API_KEY takes precedence; the OS may enforce its own keychain access policy.",
		RunE: func(cmd *cobra.Command, args []string) error {
			if timeout <= 0 {
				return usageError("timeout must be positive")
			}
			report := diagnosticReport{SchemaVersion: 1, OK: true, ConfigPath: resolvedConfigPath(deps), CredentialSource: "none", Checks: make([]diagnosticCheck, 0)}
			add := func(name, status, detail, hint string) {
				report.Checks = append(report.Checks, diagnosticCheck{name, status, detail, hint})
				if status == "error" {
					report.OK = false
				}
			}
			cfg, cfgErr := loadConfig(deps)
			if cfgErr != nil {
				add("config", "error", "Config could not be loaded or validated", "Check JSON syntax and a HTTPS base_url; HTTP is allowed on loopback only")
			} else if configExists(deps) {
				add("config", "ok", "Valid aethercli config", "")
			} else {
				add("config", "warning", "No config file; built-in defaults are active", "Optional: set default-model with `aethercli config set default-model MODEL_ID`")
			}
			if cfgErr == nil && strings.TrimSpace(cfg.DefaultModel) == "" {
				add("default_model", "warning", "No default model", "Pass --model to ask/chat, or set config default-model")
			}
			env := strings.TrimSpace(os.Getenv(secrets.EnvAPIKey)) != ""
			if env {
				report.CredentialSource = "environment"
				add("environment", "ok", "AETHER_API_KEY is set (value omitted)", "")
				add("keychain", "skipped", "Environment credential takes precedence", "")
			} else {
				add("environment", "skipped", "AETHER_API_KEY is not set", "")
			}
			key, keyErr := secrets.ResolveAPIKey(deps.Secrets)
			if keyErr != nil {
				add("credentials", "error", "No valid normal API key is available", "Set AETHER_API_KEY or use `aethercli setup` in a terminal; check OS keychain access. Connect tokens are not accepted.")
				if !env {
					add("keychain", "error", "No usable keychain credential", "Unlock/enable the OS keychain, or use AETHER_API_KEY in headless environments")
				}
			} else {
				if !env {
					report.CredentialSource = "keychain"
					add("keychain", "ok", "Stored credential available (value omitted)", "")
				}
				add("credentials", "ok", "Local key format valid; server authentication is separate", "")
			}
			if offline || cfgErr != nil {
				add("backend", "skipped", "Network checks skipped", "")
				add("authentication", "skipped", "Authentication not verified", "")
			} else {
				ctx, cancel := context.WithTimeout(cmd.Context(), timeout)
				defer cancel()
				client := deps.ClientFactory(cfg.BaseURL, "") // public reachability is not an authentication test
				if _, err := client.Models(ctx); err != nil {
					add("backend", "error", "GET /models failed ("+classifyError(err).Code+")", "Check base-url, DNS, TLS and server availability")
				} else {
					add("backend", "ok", "GET /models reachable; no provider call", "")
				}
				if keyErr != nil {
					add("authentication", "skipped", "No valid credential for authentication check", "")
				} else if raw, ok := deps.ClientFactory(cfg.BaseURL, key).(requestClient); ok {
					if err := raw.Request(ctx, http.MethodGet, "/usage", nil, false, io.Discard); err != nil {
						add("authentication", "error", "GET /usage failed ("+classifyError(err).Code+")", "Check key permissions and backend /v1/usage support")
					} else {
						add("authentication", "ok", "API key accepted by GET /usage; no provider call", "")
					}
				} else {
					add("authentication", "skipped", "Client does not implement the usage probe", "")
				}
			}
			if jsonRequested(cmd) {
				if err := writeJSON(deps.Out, report); err != nil {
					return err
				}
			} else {
				if _, err := fmt.Fprintf(deps.Out, "Config: %s\nCredentials: %s (values omitted)\n", report.ConfigPath, report.CredentialSource); err != nil {
					return err
				}
				for _, check := range report.Checks {
					if _, err := fmt.Fprintf(deps.Out, "[%s] %s: %s\n", check.Status, check.Name, check.Detail); err != nil {
						return err
					}
					if check.Hint != "" {
						if _, err := fmt.Fprintln(deps.Out, "  Hint:", check.Hint); err != nil {
							return err
						}
					}
				}
			}
			if !report.OK {
				return &CLIError{Code: "diagnostic_failed", Message: "Diagnostic checks failed; see the report on stdout", ExitCode: ExitDiagnostic}
			}
			return nil
		},
	}
	cmd.Flags().BoolVar(&offline, "offline", false, "inspect local state without network; authentication remains unverified")
	cmd.Flags().DurationVar(&timeout, "timeout", 5*time.Second, "total deadline for read-only backend checks")
	return cmd
}
