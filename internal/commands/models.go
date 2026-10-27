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
	var provider, endpoint, capability, search string
	list := func(cmd *cobra.Command, args []string) error {
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
			if capability != "" && !m.HasCapability(capability) {
				continue
			}
			if endpoint != "" {
				want := "/v1/" + strings.TrimPrefix(strings.TrimPrefix(endpoint, "/v1/"), "/")
				match := m.Endpoint == want || "/v1/"+strings.TrimPrefix(m.Endpoint, "/") == want
				for _, e := range m.SupportedEndpoints {
					if e == want || "/v1/"+strings.TrimPrefix(e, "/") == want {
						match = true
					}
				}
				if !match {
					continue
				}
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
		Example: "  aethercli models --json\n  aethercli models list --all --provider OpenAI\n  aethercli models show MODEL_ID --json",
	}
	root.PersistentFlags().BoolVar(&all, "all", false, "include non-chat catalog models")
	root.PersistentFlags().StringVar(&provider, "provider", "", "filter by exact provider name (case-insensitive)")
	root.PersistentFlags().StringVar(&endpoint, "endpoint", "", "filter by declared endpoint, e.g. responses")
	root.PersistentFlags().StringVar(&capability, "capability", "", "require an explicit true in capabilities (unknown never matches)")
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
