package commands

import (
	"fmt"
	"strings"
	"text/tabwriter"

	"github.com/Aculnaj/aethercli/internal/api"
	"github.com/spf13/cobra"
)

func newModelsCommand(deps Deps) *cobra.Command {
	var all bool
	var provider, endpoint, search string
	var capabilities []string
	list := func(cmd *cobra.Command, args []string) error {
		want := ""
		if endpoint != "" {
			if want = api.NormalizeEndpoint(endpoint); want == "" || strings.Contains(endpoint, "://") {
				return usageError("endpoint must be an API path such as chat/completions or /v1/responses")
			}
		}
		if len(capabilities) > 0 && want == "" {
			return usageError("--capability requires --endpoint: capabilities are declared per endpoint, e.g. --endpoint chat/completions --capability tool_calling")
		}
		for _, feature := range capabilities {
			if err := api.ValidateCapabilityPath(feature); err != nil {
				return usageError(err.Error())
			}
		}
		cfg, err := loadConfig(deps)
		if err != nil {
			return err
		}
		models, err := deps.ClientFactory(cfg.BaseURL, "").Models(cmd.Context())
		if err != nil {
			return err
		}
		filtered := make([]api.Model, 0)
		for _, m := range models {
			if !all && endpoint == "" && !m.SupportsChatCompletions() {
				continue
			}
			if provider != "" && !strings.EqualFold(m.OwnedBy, provider) {
				continue
			}
			if search != "" && !strings.Contains(strings.ToLower(m.ID), strings.ToLower(search)) {
				continue
			}
			if want != "" && !m.DeclaresEndpoint(want) {
				continue
			}
			if !allSupported(m, want, capabilities) {
				continue
			}
			filtered = append(filtered, m)
		}
		if jsonRequested(cmd) {
			return writeJSON(deps.Out, filtered)
		}
		writer := tabwriter.NewWriter(deps.Out, 0, 0, 2, ' ', 0)
		if _, err := fmt.Fprintln(writer, "ID\tPROVIDER\tCONTEXT\tPRICE (catalog)"); err != nil {
			return err
		}
		for _, m := range filtered {
			if _, err := fmt.Fprintf(writer, "%s\t%s\t%s\t%s\n", m.ID, m.OwnedBy, m.Context, m.OurPrice); err != nil {
				return err
			}
		}
		return writer.Flush()
	}
	root := &cobra.Command{
		Use:     "models",
		Short:   "List chat models (legacy JSON array preserved)",
		Args:    cobra.NoArgs,
		RunE:    list,
		Example: "  aethercli models --json\n  aethercli models list --all --provider OpenAI\n  aethercli models list --endpoint chat/completions --capability tool_calling --json\n  aethercli models show MODEL_ID --json",
	}
	root.PersistentFlags().BoolVar(&all, "all", false, "include non-chat catalog models")
	root.PersistentFlags().StringVar(&provider, "provider", "", "filter by exact provider name (case-insensitive)")
	root.PersistentFlags().StringVar(&endpoint, "endpoint", "", "filter by declared endpoint, e.g. responses")
	root.PersistentFlags().StringArrayVar(&capabilities, "capability", nil, "repeatable; require capabilities[--endpoint].FEATURE == \"supported\" (unknown/unsupported/missing never match), e.g. tool_calling, structured_outputs.json_schema, modalities.input.image")
	root.PersistentFlags().StringVar(&search, "search", "", "filter model IDs by substring")
	root.AddCommand(&cobra.Command{
		Use:   "list",
		Short: "List and filter catalog models",
		Args:  cobra.NoArgs,
		RunE:  list,
	})
	root.AddCommand(&cobra.Command{
		Use:   "show MODEL_ID",
		Short: "Show all declared catalog fields, prices and capabilities",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			cfg, err := loadConfig(deps)
			if err != nil {
				return err
			}
			models, err := deps.ClientFactory(cfg.BaseURL, "").Models(cmd.Context())
			if err != nil {
				return err
			}
			for _, m := range models {
				if m.ID == args[0] {
					if jsonRequested(cmd) {
						return writeJSON(deps.Out, m)
					}
					if _, err := fmt.Fprintln(deps.Out, "Catalog metadata (absent capabilities are unknown; prices are supplied by the API):"); err != nil {
						return err
					}
					return writeJSON(deps.Out, m)
				}
			}
			return &CLIError{Code: "model_not_found", Message: "Model not found in catalog; run `aethercli models list --all`", ExitCode: ExitUsage}
		},
	})
	return root
}

// allSupported requires every feature to be explicitly "supported" for the
// selected endpoint; there is no model-wide or cross-endpoint fallback.
func allSupported(m api.Model, endpoint string, features []string) bool {
	for _, feature := range features {
		if m.Capability(endpoint, feature) != api.CapabilitySupported {
			return false
		}
	}
	return true
}
