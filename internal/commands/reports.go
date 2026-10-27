package commands

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"sort"
	"time"

	"github.com/spf13/cobra"
)

func newReportCommand(deps Deps, name string) *cobra.Command {
	var period string
	var timeout time.Duration
	cmd := &cobra.Command{
		Use:   name,
		Short: "Read " + name + " reported by the API (no provider request)",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, args []string) error {
			path := "/budget-alerts"
			if name == "usage" {
				switch period {
				case "today", "7", "30", "retained":
				default:
					return usageError("period must be today, 7, 30 or retained")
				}
				path = "/usage?period=" + period
			}
			if timeout <= 0 {
				return usageError("timeout must be positive")
			}
			client, err := rawClient(deps)
			if err != nil {
				return err
			}
			ctx, cancel := context.WithTimeout(cmd.Context(), timeout)
			defer cancel()
			var buffer bytes.Buffer
			if err := client.Request(ctx, http.MethodGet, path, nil, false, &buffer); err != nil {
				return err
			}
			if jsonRequested(cmd) {
				_, err := deps.Out.Write(buffer.Bytes())
				return err
			}
			if name == "budget" {
				if _, err := fmt.Fprintln(deps.Out, "Budget alerts reported by the API (advisory; forecasts are backend estimates):"); err != nil {
					return err
				}
			} else {
				if _, err := fmt.Fprintln(deps.Out, "Usage reported by the API (account/key scopes and coverage are preserved):"); err != nil {
					return err
				}
			}
			var value any
			decoder := json.NewDecoder(&buffer)
			decoder.UseNumber()
			if err := decoder.Decode(&value); err != nil {
				return err
			}
			return printReportValue(deps, "", value)
		},
	}
	if name == "usage" {
		cmd.Flags().StringVar(&period, "period", "30", "reported usage period: today, 7, 30, retained")
	}
	cmd.Flags().DurationVar(&timeout, "timeout", 10*time.Second, "total request deadline")
	return cmd
}

// A path/value view retains scope, units, nulls and the backend's coverage and
// forecast labels instead of inventing aggregates or substituting missing zeroes.
func printReportValue(deps Deps, path string, value any) error {
	switch v := value.(type) {
	case map[string]any:
		keys := make([]string, 0, len(v))
		for key := range v {
			keys = append(keys, key)
		}
		sort.Strings(keys)
		for _, key := range keys {
			next := key
			if path != "" {
				next = path + "." + key
			}
			if err := printReportValue(deps, next, v[key]); err != nil {
				return err
			}
		}
	case []any:
		if len(v) == 0 {
			_, err := fmt.Fprintf(deps.Out, "%s: []\n", path)
			return err
		}
		for i, item := range v {
			if err := printReportValue(deps, fmt.Sprintf("%s[%d]", path, i), item); err != nil {
				return err
			}
		}
	case nil:
		_, err := fmt.Fprintf(deps.Out, "%s: unavailable\n", path)
		return err
	default:
		_, err := fmt.Fprintf(deps.Out, "%s: %v\n", path, v)
		return err
	}
	return nil
}
