package commands

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"strings"
	"sync"
	"text/tabwriter"
	"time"

	"github.com/spf13/cobra"

	"github.com/Aculnaj/aethercli/internal/api"
	"github.com/Aculnaj/aethercli/internal/config"
	"github.com/Aculnaj/aethercli/internal/prompt"
	"github.com/Aculnaj/aethercli/internal/secrets"
	"github.com/Aculnaj/aethercli/internal/update"
)

var Version = "dev"

type APIClient interface {
	Chat(ctx context.Context, req api.ChatRequest) (api.ChatResponse, error)
	StreamChat(ctx context.Context, req api.ChatRequest, onDelta func(string) error) error
	Models(ctx context.Context) ([]api.Model, error)
}

type ClientFactory func(baseURL, apiKey string) APIClient

type Deps struct {
	ConfigPath        string
	Secrets           secrets.Store
	In                io.Reader
	Out               io.Writer
	Err               io.Writer
	ClientFactory     ClientFactory
	StdinHasData      func() bool
	UpdateChecker     update.Checker
	UpdateInstaller   update.Installer
	CurrentVersion    string
	DefaultInstallDir string
	Now               func() time.Time
}

type askOptions struct {
	model       string
	temperature float64
	maxTokens   int
	stream      bool
	jsonOut     bool
}

func NewRootCommand(deps Deps) *cobra.Command {
	deps = normalizeDeps(deps)

	root := &cobra.Command{
		Use:           "aether",
		Short:         "AetherAPI command-line client",
		Version:       deps.CurrentVersion,
		SilenceUsage:  true,
		SilenceErrors: true,
		PersistentPreRunE: func(cmd *cobra.Command, args []string) error {
			return runAutoUpdateCheck(cmd.Context(), deps, cmd)
		},
		RunE: func(cmd *cobra.Command, args []string) error {
			return runInteractive(cmd.Context(), deps)
		},
	}
	root.SetVersionTemplate("{{.Version}}\n")
	root.SetIn(deps.In)
	root.SetOut(deps.Out)
	root.SetErr(deps.Err)

	root.AddCommand(newSetupCommand(deps))
	root.AddCommand(newAskCommand(deps))
	root.AddCommand(newModelsCommand(deps))
	root.AddCommand(newConfigCommand(deps))
	root.AddCommand(newUpdateCommand(deps))
	return root
}

func newSetupCommand(deps Deps) *cobra.Command {
	return &cobra.Command{
		Use:   "setup",
		Short: "Store your AetherAPI key and default model",
		RunE: func(cmd *cobra.Command, args []string) error {
			reader := bufio.NewReader(deps.In)
			_, _, err := runSetup(deps, reader)
			return err
		},
	}
}

func newAskCommand(deps Deps) *cobra.Command {
	opts := askOptions{}
	cmd := &cobra.Command{
		Use:   "ask [prompt]",
		Short: "Send one prompt to an AetherAPI chat model",
		RunE: func(cmd *cobra.Command, args []string) error {
			reader := bufio.NewReader(deps.In)
			cfg, apiKey, err := ensureConfigured(deps, reader, true)
			if err != nil {
				return err
			}

			resolvedPrompt, err := prompt.Resolve(prompt.Options{
				Args:         args,
				Stdin:        deps.In,
				StdinHasData: deps.StdinHasData(),
				Ask:          lineAsker(deps.Err, reader),
			})
			if err != nil {
				return err
			}

			model := strings.TrimSpace(opts.model)
			if model == "" {
				model = strings.TrimSpace(cfg.DefaultModel)
			}
			if model == "" {
				return fmt.Errorf("missing model: pass --model or run `aether setup`")
			}

			req := api.ChatRequest{
				Model:  model,
				Prompt: resolvedPrompt,
			}
			if cmd.Flags().Changed("temperature") {
				req.Temperature = &opts.temperature
			}
			if cmd.Flags().Changed("max-tokens") {
				req.MaxTokens = &opts.maxTokens
			}
			return runAsk(cmd.Context(), deps, cfg, apiKey, req, opts)
		},
	}
	cmd.Flags().StringVarP(&opts.model, "model", "m", "", "chat model ID")
	cmd.Flags().Float64Var(&opts.temperature, "temperature", 1, "sampling temperature")
	cmd.Flags().IntVar(&opts.maxTokens, "max-tokens", 0, "maximum output tokens")
	cmd.Flags().BoolVar(&opts.stream, "stream", false, "stream response deltas")
	cmd.Flags().BoolVar(&opts.jsonOut, "json", false, "print JSON output")
	return cmd
}

func newModelsCommand(deps Deps) *cobra.Command {
	var jsonOut bool
	cmd := &cobra.Command{
		Use:   "models",
		Short: "List AetherAPI chat models",
		RunE: func(cmd *cobra.Command, args []string) error {
			cfg, err := config.Load(deps.ConfigPath)
			if err != nil {
				return err
			}
			apiKey, err := secrets.ResolveAPIKey(deps.Secrets)
			if errors.Is(err, secrets.ErrMissingAPIKey) {
				apiKey = ""
			} else if err != nil {
				return err
			}

			client := deps.ClientFactory(cfg.BaseURL, apiKey)
			models, err := client.Models(cmd.Context())
			if err != nil {
				return userFacingError(err)
			}
			models = api.FilterChatModels(models)
			if jsonOut {
				encoder := json.NewEncoder(deps.Out)
				encoder.SetIndent("", "  ")
				return encoder.Encode(models)
			}
			return printModels(deps.Out, models)
		},
	}
	cmd.Flags().BoolVar(&jsonOut, "json", false, "print models as JSON")
	return cmd
}

func newConfigCommand(deps Deps) *cobra.Command {
	cmd := &cobra.Command{
		Use:   "config",
		Short: "Manage local Aether CLI config",
	}
	cmd.AddCommand(&cobra.Command{
		Use:   "get [default-model|base-url]",
		Short: "Print config",
		Args:  cobra.MaximumNArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			cfg, err := config.Load(deps.ConfigPath)
			if err != nil {
				return err
			}
			if len(args) == 0 {
				encoder := json.NewEncoder(deps.Out)
				encoder.SetIndent("", "  ")
				return encoder.Encode(cfg)
			}
			switch args[0] {
			case "default-model":
				_, err = fmt.Fprintln(deps.Out, cfg.DefaultModel)
			case "base-url":
				_, err = fmt.Fprintln(deps.Out, cfg.BaseURL)
			default:
				err = fmt.Errorf("unknown config key %q", args[0])
			}
			return err
		},
	})
	cmd.AddCommand(&cobra.Command{
		Use:   "set [default-model|base-url] [value]",
		Short: "Set config value",
		Args:  cobra.ExactArgs(2),
		RunE: func(cmd *cobra.Command, args []string) error {
			cfg, err := config.Load(deps.ConfigPath)
			if err != nil {
				return err
			}
			switch args[0] {
			case "default-model":
				cfg.DefaultModel = strings.TrimSpace(args[1])
				if cfg.DefaultModel == "" {
					return fmt.Errorf("default model cannot be empty")
				}
			case "base-url":
				cfg.BaseURL = strings.TrimRight(strings.TrimSpace(args[1]), "/")
				if cfg.BaseURL == "" {
					return fmt.Errorf("base URL cannot be empty")
				}
			default:
				return fmt.Errorf("unknown config key %q", args[0])
			}
			return config.Save(deps.ConfigPath, cfg)
		},
	})
	cmd.AddCommand(&cobra.Command{
		Use:   "clear [key|default-model|base-url]",
		Short: "Clear stored key or config",
		Args:  cobra.MaximumNArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			if len(args) == 0 {
				if err := config.Delete(deps.ConfigPath); err != nil {
					return err
				}
				return deps.Secrets.Delete()
			}
			cfg, err := config.Load(deps.ConfigPath)
			if err != nil {
				return err
			}
			switch args[0] {
			case "key":
				return deps.Secrets.Delete()
			case "default-model":
				cfg.DefaultModel = ""
			case "base-url":
				cfg.BaseURL = config.DefaultBaseURL
			default:
				return fmt.Errorf("unknown config key %q", args[0])
			}
			return config.Save(deps.ConfigPath, cfg)
		},
	})
	return cmd
}

func newUpdateCommand(deps Deps) *cobra.Command {
	var installDir string
	cmd := &cobra.Command{
		Use:   "update",
		Short: "Update Aether CLI to the latest GitHub release",
		RunE: func(cmd *cobra.Command, args []string) error {
			release, err := deps.UpdateChecker.Latest(cmd.Context())
			if err != nil {
				return fmt.Errorf("check latest release: %w", err)
			}
			if strings.TrimSpace(release.Version) == "" {
				return fmt.Errorf("latest release did not include a version")
			}
			if deps.CurrentVersion != "dev" && !update.IsNewerVersion(deps.CurrentVersion, release.Version) {
				_, err = fmt.Fprintln(deps.Out, "No update found.")
				return err
			}

			targetDir := strings.TrimSpace(installDir)
			if targetDir == "" {
				targetDir = deps.DefaultInstallDir
			}
			result, err := deps.UpdateInstaller.Install(cmd.Context(), update.InstallOptions{
				Version:    release.Version,
				InstallDir: targetDir,
			})
			if err != nil {
				return err
			}
			_, err = fmt.Fprintf(deps.Out, "Updated aether to %s\nBinary: %s\n", release.Version, result.Path)
			return err
		},
	}
	cmd.Flags().StringVar(&installDir, "install-dir", "", "directory where the aether binary should be installed")
	return cmd
}

func runInteractive(ctx context.Context, deps Deps) error {
	reader := bufio.NewReader(deps.In)
	cfg, apiKey, err := ensureConfigured(deps, reader, true)
	if err != nil {
		return err
	}
	resolvedPrompt, err := prompt.Resolve(prompt.Options{
		Ask: lineAsker(deps.Err, reader),
	})
	if err != nil {
		return err
	}
	req := api.ChatRequest{
		Model:  cfg.DefaultModel,
		Prompt: resolvedPrompt,
	}
	return runAsk(ctx, deps, cfg, apiKey, req, askOptions{})
}

func runAsk(ctx context.Context, deps Deps, cfg config.Config, apiKey string, req api.ChatRequest, opts askOptions) error {
	if opts.stream && opts.jsonOut {
		return fmt.Errorf("cannot combine --stream and --json")
	}

	client := deps.ClientFactory(cfg.BaseURL, apiKey)
	if opts.stream {
		stopThinking := startThinkingSpinner(deps.Err, thinkingSpinnerInterval)
		firstDelta := true
		err := client.StreamChat(ctx, req, func(delta string) error {
			if firstDelta {
				stopThinking()
				firstDelta = false
			}
			if _, err := fmt.Fprint(deps.Out, delta); err != nil {
				return err
			}
			return flushIfSupported(deps.Out)
		})
		if firstDelta {
			stopThinking()
		}
		if err != nil {
			return userFacingError(err)
		}
		if _, err = fmt.Fprintln(deps.Out); err != nil {
			return err
		}
		return flushIfSupported(deps.Out)
	}

	stopThinking := func() {}
	if !opts.jsonOut {
		stopThinking = startThinkingSpinner(deps.Err, thinkingSpinnerInterval)
	}

	resp, err := client.Chat(ctx, req)
	stopThinking()
	if err != nil {
		return userFacingError(err)
	}
	if opts.jsonOut {
		encoder := json.NewEncoder(deps.Out)
		encoder.SetIndent("", "  ")
		return encoder.Encode(resp)
	}
	_, err = fmt.Fprintln(deps.Out, resp.Content)
	return err
}

func ensureConfigured(deps Deps, reader *bufio.Reader, requireDefaultModel bool) (config.Config, string, error) {
	cfg, err := config.Load(deps.ConfigPath)
	if err != nil {
		return config.Config{}, "", err
	}

	apiKey, keyErr := secrets.ResolveAPIKey(deps.Secrets)
	if errors.Is(keyErr, secrets.ErrMissingAPIKey) || (requireDefaultModel && strings.TrimSpace(cfg.DefaultModel) == "") {
		return runSetup(deps, reader)
	}
	if keyErr != nil {
		return config.Config{}, "", keyErr
	}
	return cfg, apiKey, nil
}

func runSetup(deps Deps, reader *bufio.Reader) (config.Config, string, error) {
	ask := lineAsker(deps.Err, reader)

	apiKey, err := ask("AetherAPI key")
	if err != nil {
		return config.Config{}, "", err
	}
	apiKey, err = secrets.NormalizeAPIKey(apiKey)
	if err != nil {
		return config.Config{}, "", err
	}
	if err := deps.Secrets.Set(apiKey); err != nil {
		return config.Config{}, "", err
	}

	defaultModel, err := ask("Default model")
	if err != nil {
		return config.Config{}, "", err
	}
	defaultModel = strings.TrimSpace(defaultModel)
	if defaultModel == "" {
		return config.Config{}, "", fmt.Errorf("default model cannot be empty")
	}

	cfg, err := config.Load(deps.ConfigPath)
	if err != nil {
		return config.Config{}, "", err
	}
	cfg.DefaultModel = defaultModel
	if err := config.Save(deps.ConfigPath, cfg); err != nil {
		return config.Config{}, "", err
	}

	_, _ = fmt.Fprintln(deps.Err, "Setup complete.")
	return cfg, apiKey, nil
}

func lineAsker(out io.Writer, reader *bufio.Reader) func(label string) (string, error) {
	return func(label string) (string, error) {
		if _, err := fmt.Fprintf(out, "%s: ", label); err != nil {
			return "", err
		}
		value, err := reader.ReadString('\n')
		if errors.Is(err, io.EOF) && value != "" {
			return strings.TrimSpace(value), nil
		}
		if err != nil {
			return "", err
		}
		return strings.TrimSpace(value), nil
	}
}

func printModels(out io.Writer, models []api.Model) error {
	writer := tabwriter.NewWriter(out, 0, 0, 2, ' ', 0)
	if _, err := fmt.Fprintln(writer, "ID\tPROVIDER\tCONTEXT\tPRICE"); err != nil {
		return err
	}
	for _, model := range models {
		if _, err := fmt.Fprintf(writer, "%s\t%s\t%s\t%s\n", model.ID, model.OwnedBy, model.Context, model.OurPrice); err != nil {
			return err
		}
	}
	return writer.Flush()
}

func runAutoUpdateCheck(ctx context.Context, deps Deps, cmd *cobra.Command) error {
	if shouldSkipAutoUpdateCheck(deps, cmd) {
		return nil
	}

	cfg, err := config.Load(deps.ConfigPath)
	if err != nil {
		return nil
	}
	if !updateCheckDue(cfg, deps.Now()) {
		return nil
	}

	checkCtx, cancel := context.WithTimeout(ctx, 2*time.Second)
	defer cancel()

	release, err := deps.UpdateChecker.Latest(checkCtx)
	if err != nil {
		return nil
	}

	now := deps.Now().UTC().Format(time.RFC3339)
	cfg.Update = &config.UpdateState{
		LastCheckedAt:   now,
		LastSeenVersion: release.Version,
	}
	_ = config.Save(deps.ConfigPath, cfg)

	if update.IsNewerVersion(deps.CurrentVersion, release.Version) {
		_, _ = fmt.Fprintf(deps.Err, "Update available: aether %s -> %s\nRun `aether update` to install it.\n", deps.CurrentVersion, release.Version)
	}
	return nil
}

func shouldSkipAutoUpdateCheck(deps Deps, cmd *cobra.Command) bool {
	if deps.CurrentVersion == "" || deps.CurrentVersion == "dev" {
		return true
	}
	if os.Getenv("AETHER_NO_UPDATE_CHECK") != "" {
		return true
	}
	if cmd.Name() == "update" {
		return true
	}
	return false
}

func updateCheckDue(cfg config.Config, now time.Time) bool {
	if cfg.Update == nil || strings.TrimSpace(cfg.Update.LastCheckedAt) == "" {
		return true
	}
	lastCheckedAt, err := time.Parse(time.RFC3339, cfg.Update.LastCheckedAt)
	if err != nil {
		return true
	}
	return now.Sub(lastCheckedAt) >= 24*time.Hour
}

type flushableWriter interface {
	Flush() error
}

const thinkingSpinnerInterval = 120 * time.Millisecond

var thinkingSpinnerFrames = []string{"|", "/", "-", "\\"}

func startThinkingSpinner(out io.Writer, interval time.Duration) func() {
	if out == nil {
		return func() {}
	}
	if interval <= 0 {
		interval = thinkingSpinnerInterval
	}

	done := make(chan struct{})
	stopped := make(chan struct{})
	var once sync.Once
	frame := 0

	writeFrame := func() {
		_, _ = fmt.Fprintf(out, "\rThinking %s", thinkingSpinnerFrames[frame%len(thinkingSpinnerFrames)])
		_ = flushIfSupported(out)
		frame++
	}

	writeFrame()
	go func() {
		defer close(stopped)
		ticker := time.NewTicker(interval)
		defer ticker.Stop()
		for {
			select {
			case <-ticker.C:
				writeFrame()
			case <-done:
				return
			}
		}
	}()

	return func() {
		once.Do(func() {
			close(done)
			<-stopped
			_, _ = fmt.Fprint(out, "\r            \r")
			_ = flushIfSupported(out)
		})
	}
}

func flushIfSupported(out io.Writer) error {
	writer, ok := out.(flushableWriter)
	if !ok {
		return nil
	}
	return writer.Flush()
}

func userFacingError(err error) error {
	var apiErr *api.APIError
	if errors.As(err, &apiErr) {
		return errors.New(apiErr.UserMessage())
	}
	return err
}

func normalizeDeps(deps Deps) Deps {
	if deps.Secrets == nil {
		deps.Secrets = secrets.DefaultStore()
	}
	if deps.In == nil {
		deps.In = os.Stdin
	}
	if deps.Out == nil {
		deps.Out = os.Stdout
	}
	if deps.Err == nil {
		deps.Err = os.Stderr
	}
	if deps.ClientFactory == nil {
		deps.ClientFactory = func(baseURL, apiKey string) APIClient {
			return api.NewClient(api.ClientOptions{
				BaseURL: baseURL,
				APIKey:  apiKey,
			})
		}
	}
	if deps.StdinHasData == nil {
		deps.StdinHasData = stdinHasData
	}
	if deps.UpdateChecker == nil {
		deps.UpdateChecker = update.NewDefaultChecker()
	}
	if deps.UpdateInstaller == nil {
		deps.UpdateInstaller = update.NewDefaultInstaller()
	}
	if deps.CurrentVersion == "" {
		deps.CurrentVersion = Version
	}
	if deps.Now == nil {
		deps.Now = time.Now
	}
	return deps
}

func stdinHasData() bool {
	info, err := os.Stdin.Stat()
	if err != nil {
		return false
	}
	return info.Mode()&os.ModeCharDevice == 0
}
