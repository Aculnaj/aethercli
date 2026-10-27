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
	"time"

	"github.com/charmbracelet/x/term"
	"github.com/spf13/cobra"

	"github.com/Aculnaj/aethercli/internal/api"
	"github.com/Aculnaj/aethercli/internal/config"
	"github.com/Aculnaj/aethercli/internal/prompt"
	"github.com/Aculnaj/aethercli/internal/secrets"
	aethertui "github.com/Aculnaj/aethercli/internal/tui"
	"github.com/Aculnaj/aethercli/internal/update"
)

var Version = "dev"

type APIClient interface {
	Chat(ctx context.Context, req api.ChatRequest) (api.ChatResponse, error)
	StreamChat(ctx context.Context, req api.ChatRequest, onDelta func(string) error) error
	Models(ctx context.Context) ([]api.Model, error)
}

type ClientFactory func(baseURL, apiKey string) APIClient

type TUIRunner func(ctx context.Context, opts TUIOptions) error

type TUIOptions struct {
	ConfigPath    string
	Config        config.Config
	APIKey        string
	Model         string
	Resume        bool
	SessionID     string
	In            io.Reader
	Out           io.Writer
	Err           io.Writer
	ClientFactory ClientFactory
	Now           func() time.Time
}

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
	TUIRunner         TUIRunner
	Interactive       func() bool
	configPath        func() string
}

type askOptions struct {
	model       string
	temperature float64
	maxTokens   int
	stream      bool
	jsonOut     bool
	estimate    bool
	files       []string
	contextDirs []string
}

func NewRootCommand(deps Deps) *cobra.Command {
	deps = normalizeDeps(deps)
	var jsonOut, nonInteractive bool
	terminal := deps.Interactive
	deps.Interactive = func() bool { return !jsonOut && !nonInteractive && terminal() }

	root := &cobra.Command{
		Use:           "aethercli",
		Short:         "AetherAPI command-line client",
		Version:       deps.CurrentVersion,
		SilenceUsage:  true,
		SilenceErrors: true,
		PersistentPreRunE: func(cmd *cobra.Command, args []string) error {
			if err := validateInvocation(cmd, args, deps); err != nil {
				return err
			}
			return runAutoUpdateCheck(cmd.Context(), deps, cmd)
		},
		RunE: func(cmd *cobra.Command, args []string) error {
			return runInteractive(cmd.Context(), deps)
		},
	}
	root.PersistentFlags().BoolVar(&jsonOut, "json", false, "print JSON data and structured errors (streaming uses SSE)")
	root.PersistentFlags().BoolVar(&nonInteractive, "non-interactive", false, "disable prompts, TUI, spinners and automatic update checks")
	root.PersistentFlags().StringVar(&deps.ConfigPath, "config", deps.ConfigPath, "path to aethercli config (never contains the API key)")
	deps.configPath = func() string { return deps.ConfigPath }
	root.SetVersionTemplate(`{{if eq (.Flag "json").Value.String "true"}}{"version":{{printf "%q" .Version}}}{{else}}{{.Version}}{{end}}` + "\n")
	root.SetIn(deps.In)
	root.SetOut(deps.Out)
	root.SetErr(deps.Err)

	root.AddCommand(newSetupCommand(deps))
	root.AddCommand(newAskCommand(deps))
	root.AddCommand(newChatCommand(deps))
	root.AddCommand(newTUICommand(deps))
	root.AddCommand(newSessionsCommand(deps))
	root.AddCommand(newModelsCommand(deps))
	root.AddCommand(newConfigCommand(deps))
	root.AddCommand(newUpdateCommand(deps))
	root.AddCommand(newDiagnosticCommand(deps, "status"), newDiagnosticCommand(deps, "doctor"))
	root.AddCommand(newReportCommand(deps, "usage"), newReportCommand(deps, "budget"))
	root.AddCommand(newAPICommand(deps), newSandboxCommand(deps))
	installMachineHelp(root)
	return root
}

func newSetupCommand(deps Deps) *cobra.Command {
	return &cobra.Command{
		Use:   "setup",
		Short: "Store your AetherAPI key and default model",
		Args:  cobra.NoArgs,
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
			opts.jsonOut = jsonRequested(cmd)
			reader := bufio.NewReader(deps.In)
			cfg, apiKey, err := ensureConfigured(deps, reader, strings.TrimSpace(opts.model) == "")
			if err != nil {
				return err
			}

			resolvedPrompt, err := prompt.Resolve(prompt.Options{
				Args:         args,
				Stdin:        deps.In,
				StdinHasData: deps.StdinHasData(),
				Ask:          promptAsker(deps, reader),
			})
			if err != nil {
				return err
			}

			model := strings.TrimSpace(opts.model)
			if model == "" {
				model = strings.TrimSpace(cfg.DefaultModel)
			}
			if model == "" {
				return fmt.Errorf("missing model: pass --model or run `aethercli setup`")
			}
			resolvedPrompt, err = addPromptContext(resolvedPrompt, opts.files, opts.contextDirs)
			if err != nil {
				return err
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
			if opts.estimate {
				return runEstimate(cmd.Context(), deps, cfg, apiKey, req, opts.jsonOut)
			}
			return runAsk(cmd.Context(), deps, cfg, apiKey, req, opts)
		},
	}
	cmd.Flags().StringVarP(&opts.model, "model", "m", "", "chat model ID")
	cmd.Flags().Float64Var(&opts.temperature, "temperature", 1, "sampling temperature")
	cmd.Flags().IntVar(&opts.maxTokens, "max-tokens", 0, "maximum output tokens")
	cmd.Flags().BoolVar(&opts.stream, "stream", false, "stream response deltas")
	cmd.Flags().BoolVar(&opts.estimate, "estimate", false, "print token and cost estimate without sending a chat request")
	cmd.Flags().StringArrayVarP(&opts.files, "file", "f", nil, "include a file as prompt context")
	cmd.Flags().StringArrayVar(&opts.contextDirs, "context", nil, "include a directory as prompt context")
	return cmd
}

func newUpdateCommand(deps Deps) *cobra.Command {
	var installDir string
	cmd := &cobra.Command{
		Use:   "update",
		Short: "Update aethercli to the latest GitHub release",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, args []string) error {
			release, err := deps.UpdateChecker.Latest(cmd.Context())
			if err != nil {
				return fmt.Errorf("check latest release: %w", err)
			}
			if strings.TrimSpace(release.Version) == "" {
				return fmt.Errorf("latest release did not include a version")
			}
			if deps.CurrentVersion != "dev" && !update.IsNewerVersion(deps.CurrentVersion, release.Version) {
				if jsonRequested(cmd) {
					return writeJSON(deps.Out, map[string]any{"updated": false, "version": deps.CurrentVersion})
				}
				_, err = fmt.Fprintln(deps.Out, "You already have the newest version.")
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
			if jsonRequested(cmd) {
				return writeJSON(deps.Out, map[string]any{"updated": true, "version": release.Version, "path": result.Path})
			}
			_, err = fmt.Fprintf(deps.Out, "Updated aethercli to %s\nBinary: %s\n", release.Version, result.Path)
			return err
		},
	}
	cmd.Flags().StringVar(&installDir, "install-dir", "", "directory where the aethercli binary should be installed")
	return cmd
}

func runInteractive(ctx context.Context, deps Deps) error {
	reader := bufio.NewReader(deps.In)
	cfg, apiKey, err := ensureConfigured(deps, reader, true)
	if err != nil {
		return err
	}
	resolvedPrompt, err := prompt.Resolve(prompt.Options{
		Ask: promptAsker(deps, reader),
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

func runTUI(ctx context.Context, deps Deps, cfg config.Config, apiKey string, opts chatOptions) error {
	model := strings.TrimSpace(opts.model)
	if model == "" {
		model = strings.TrimSpace(cfg.DefaultModel)
	}
	if model == "" {
		return fmt.Errorf("missing model: pass --model or run `aethercli setup`")
	}
	return deps.TUIRunner(ctx, TUIOptions{
		ConfigPath:    deps.path(),
		Config:        cfg,
		APIKey:        apiKey,
		Model:         model,
		Resume:        opts.resume,
		SessionID:     opts.sessionID,
		In:            deps.In,
		Out:           deps.Out,
		Err:           deps.Err,
		ClientFactory: deps.ClientFactory,
		Now:           deps.Now,
	})
}

func runAsk(ctx context.Context, deps Deps, cfg config.Config, apiKey string, req api.ChatRequest, opts askOptions) error {
	if opts.stream && opts.jsonOut {
		return fmt.Errorf("cannot combine --stream and --json")
	}

	client := deps.ClientFactory(cfg.BaseURL, apiKey)
	if opts.stream {
		stopThinking := thinkingSpinner(deps)
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
	if !opts.jsonOut && deps.Interactive() {
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
	cfg, err := loadConfig(deps)
	if err != nil {
		return config.Config{}, "", err
	}

	apiKey, keyErr := secrets.ResolveAPIKey(deps.Secrets)
	if keyErr != nil && !errors.Is(keyErr, secrets.ErrMissingAPIKey) {
		return config.Config{}, "", authError(keyErr)
	}
	if errors.Is(keyErr, secrets.ErrMissingAPIKey) || (requireDefaultModel && strings.TrimSpace(cfg.DefaultModel) == "") {
		if deps.Interactive() {
			return runSetup(deps, reader)
		}
		if keyErr != nil {
			return config.Config{}, "", authError(keyErr)
		}
		return config.Config{}, "", usageError("missing model: pass --model or set config default-model")
	}
	if keyErr != nil {
		return config.Config{}, "", keyErr
	}
	return cfg, apiKey, nil
}

func runSetup(deps Deps, reader *bufio.Reader) (config.Config, string, error) {
	cfg, err := loadConfig(deps)
	if err != nil {
		return config.Config{}, "", err
	}
	ask := lineAsker(deps.Err, reader)

	var apiKey string
	if input, ok := deps.In.(*os.File); ok && term.IsTerminal(input.Fd()) {
		if _, err := fmt.Fprint(deps.Err, "AetherAPI key: "); err != nil {
			return config.Config{}, "", err
		}
		value, readErr := term.ReadPassword(input.Fd())
		_, _ = fmt.Fprintln(deps.Err)
		apiKey, err = string(value), readErr
	} else {
		apiKey, err = ask("AetherAPI key")
	}
	if err != nil {
		return config.Config{}, "", err
	}
	apiKey, err = secrets.NormalizeAPIKey(apiKey)
	if err != nil {
		return config.Config{}, "", authError(err)
	}
	defaultModel, err := ask("Default model")
	if err != nil {
		return config.Config{}, "", err
	}
	defaultModel = strings.TrimSpace(defaultModel)
	if defaultModel == "" {
		return config.Config{}, "", usageError("default model cannot be empty")
	}

	cfg.DefaultModel = defaultModel
	if err := config.Validate(cfg); err != nil {
		return config.Config{}, "", usageError(err.Error())
	}
	if err := deps.Secrets.Set(apiKey); err != nil {
		return config.Config{}, "", authError(err)
	}
	if err := config.Save(deps.path(), cfg); err != nil {
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

func runAutoUpdateCheck(ctx context.Context, deps Deps, cmd *cobra.Command) error {
	if shouldSkipAutoUpdateCheck(deps, cmd) {
		return nil
	}

	cfg, err := loadConfig(deps)
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
	_ = config.Save(deps.path(), cfg)

	if update.IsNewerVersion(deps.CurrentVersion, release.Version) {
		_, _ = fmt.Fprintf(deps.Err, "Update available: aethercli %s -> %s\nRun `aethercli update` to install it.\n", deps.CurrentVersion, release.Version)
	}
	return nil
}

func shouldSkipAutoUpdateCheck(deps Deps, cmd *cobra.Command) bool {
	if !deps.Interactive() || (cmd.Name() != "ask" && cmd.Name() != "chat" && cmd.Name() != "tui" && cmd.Parent() != nil) {
		return true
	}
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
		return classifyError(apiErr)
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
	if deps.Interactive == nil {
		deps.Interactive = func() bool { return terminalIO(deps.In, deps.Out) }
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
	if deps.TUIRunner == nil {
		deps.TUIRunner = defaultTUIRunner
	}
	return deps
}

func defaultTUIRunner(ctx context.Context, opts TUIOptions) error {
	return aethertui.Run(ctx, aethertui.Options{
		ConfigPath: opts.ConfigPath,
		Config:     opts.Config,
		APIKey:     opts.APIKey,
		Model:      opts.Model,
		Resume:     opts.Resume,
		SessionID:  opts.SessionID,
		In:         opts.In,
		Out:        opts.Out,
		Err:        opts.Err,
		ClientFactory: func(baseURL, apiKey string) aethertui.Client {
			return opts.ClientFactory(baseURL, apiKey)
		},
		Now: opts.Now,
	})
}

func stdinHasData() bool {
	info, err := os.Stdin.Stat()
	if err != nil {
		return false
	}
	return info.Mode()&os.ModeCharDevice == 0
}
