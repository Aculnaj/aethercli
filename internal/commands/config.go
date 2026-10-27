package commands

import (
	"fmt"
	"os"
	"strings"

	"github.com/Aculnaj/aethercli/internal/config"
	"github.com/spf13/cobra"
)

func newConfigCommand(deps Deps) *cobra.Command {
	root := &cobra.Command{
		Use:   "config",
		Short: "Inspect and manage only aethercli's non-secret configuration",
	}
	var dryRun bool
	root.PersistentFlags().BoolVar(&dryRun, "dry-run", false, "preview changes without writing config or keychain")
	root.AddCommand(&cobra.Command{
		Use:   "get [default-model|base-url]",
		Short: "Print effective config or one value (never credentials)",
		Args:  cobra.MaximumNArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			cfg, err := loadConfig(deps)
			if err != nil {
				return err
			}
			if len(args) == 0 {
				return writeJSON(deps.Out, cfg)
			}
			var value string
			switch args[0] {
			case "default-model":
				value = cfg.DefaultModel
			case "base-url":
				value = cfg.BaseURL
			default:
				return usageError("unknown config key")
			}
			if jsonRequested(cmd) {
				return writeJSON(deps.Out, map[string]string{args[0]: value})
			}
			_, err = fmt.Fprintln(deps.Out, value)
			return err
		},
	})
	root.AddCommand(&cobra.Command{
		Use:   "validate",
		Short: "Validate the local config without network or changes",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, args []string) error {
			_, err := loadConfig(deps)
			if err != nil {
				return err
			}
			if jsonRequested(cmd) {
				return writeJSON(deps.Out, map[string]any{"valid": true, "path": resolvedConfigPath(deps)})
			}
			_, err = fmt.Fprintln(deps.Out, "Config valid:", resolvedConfigPath(deps))
			return err
		},
	})
	root.AddCommand(&cobra.Command{
		Use:   "set [default-model|base-url] VALUE",
		Short: "Set a local value; use --dry-run to preview",
		Args:  cobra.ExactArgs(2),
		RunE: func(cmd *cobra.Command, args []string) error {
			before, err := readConfig(deps)
			if err != nil {
				return err
			}
			after := before
			value := strings.TrimSpace(args[1])
			if value == "" {
				return usageError("config value cannot be empty")
			}
			switch args[0] {
			case "default-model":
				after.DefaultModel = value
			case "base-url":
				after.BaseURL = strings.TrimRight(value, "/")
			default:
				return usageError("unknown config key")
			}
			if err := config.Validate(after); err != nil {
				return usageError(err.Error())
			}
			if !dryRun {
				if err := config.Save(deps.path(), after); err != nil {
					return err
				}
			}
			if jsonRequested(cmd) || dryRun {
				return writeJSON(deps.Out, map[string]any{"dry_run": dryRun, "before": configPreview(before), "after": after, "path": resolvedConfigPath(deps)})
			}
			return nil
		},
	})
	root.AddCommand(&cobra.Command{
		Use:   "clear [key|default-model|base-url]",
		Short: "Clear aethercli config or its keychain entry; preview with --dry-run",
		Args:  cobra.MaximumNArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			before, err := readConfig(deps)
			if err != nil {
				return err
			}
			after := before
			target := "all"
			if len(args) > 0 {
				target = args[0]
			}
			switch target {
			case "all":
				after = config.Config{BaseURL: config.DefaultBaseURL}
			case "key":
			case "default-model":
				after.DefaultModel = ""
			case "base-url":
				after.BaseURL = config.DefaultBaseURL
			default:
				return usageError("unknown config key")
			}
			if err := config.Validate(after); err != nil {
				return usageError(err.Error())
			}
			if !dryRun {
				if target == "all" {
					err = config.Delete(deps.path())
				} else if target != "key" {
					err = config.Save(deps.path(), after)
				}
				if err != nil {
					return err
				}
				if target == "all" || target == "key" {
					if err := deps.Secrets.Delete(); err != nil {
						return authError(err)
					}
				}
			}
			if jsonRequested(cmd) || dryRun {
				return writeJSON(deps.Out, map[string]any{"dry_run": dryRun, "target": target, "before": configPreview(before), "after": after, "path": resolvedConfigPath(deps), "environment_unchanged": true})
			}
			return nil
		},
	})
	return root
}
func resolvedConfigPath(deps Deps) string {
	if deps.path() != "" {
		return deps.path()
	}
	p, _ := config.DefaultPath()
	return p
}

func configExists(deps Deps) bool { _, err := os.Stat(resolvedConfigPath(deps)); return err == nil }

// Invalid old config may contain URL credentials; allow repair without echoing
// those values in a preview or a JSON mutation result.
func configPreview(cfg config.Config) any {
	if config.Validate(cfg) != nil {
		return map[string]bool{"invalid": true}
	}
	return cfg
}
