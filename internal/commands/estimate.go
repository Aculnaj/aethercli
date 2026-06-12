package commands

import (
	"context"
	"fmt"
	"math"
	"regexp"
	"strings"

	"github.com/Aculnaj/aethercli/internal/api"
	"github.com/Aculnaj/aethercli/internal/config"
)

var priceNumberPattern = regexp.MustCompile(`\$?\s*([0-9]+(?:\.[0-9]+)?)`)

func runEstimate(ctx context.Context, deps Deps, cfg config.Config, apiKey string, req api.ChatRequest) error {
	client := deps.ClientFactory(cfg.BaseURL, apiKey)
	models, err := client.Models(ctx)
	if err != nil {
		return userFacingError(err)
	}
	model := findModel(api.FilterChatModels(models), req.Model)
	inputTokens := estimateTokens(req.Prompt)

	outputTokens := 0
	if req.MaxTokens != nil && *req.MaxTokens > 0 {
		outputTokens = *req.MaxTokens
	}

	if _, err := fmt.Fprintf(deps.Out, "Model: %s\n", req.Model); err != nil {
		return err
	}
	if _, err := fmt.Fprintf(deps.Out, "Estimated input tokens: %d\n", inputTokens); err != nil {
		return err
	}
	if outputTokens > 0 {
		if _, err := fmt.Fprintf(deps.Out, "Max output tokens: %d\n", outputTokens); err != nil {
			return err
		}
	} else if _, err := fmt.Fprintln(deps.Out, "Max output tokens: not set"); err != nil {
		return err
	}

	if model.ID == "" || strings.TrimSpace(model.OurPrice) == "" {
		_, err := fmt.Fprintln(deps.Out, "Estimated cost: unavailable; model price metadata is missing")
		return err
	}

	if _, err := fmt.Fprintf(deps.Out, "Price: %s\n", model.OurPrice); err != nil {
		return err
	}
	inputPerMillion, outputPerMillion, ok := parsePricePerMillion(model.OurPrice)
	if !ok {
		_, err := fmt.Fprintln(deps.Out, "Estimated cost: unavailable; could not parse model price")
		return err
	}

	inputCost := float64(inputTokens) / 1_000_000 * inputPerMillion
	outputCost := float64(outputTokens) / 1_000_000 * outputPerMillion
	_, err = fmt.Fprintf(deps.Out, "Estimated cost: $%.6f\n", inputCost+outputCost)
	return err
}

func findModel(models []api.Model, id string) api.Model {
	for _, model := range models {
		if model.ID == id {
			return model
		}
	}
	return api.Model{}
}

func estimateTokens(text string) int {
	text = strings.TrimSpace(text)
	if text == "" {
		return 0
	}
	return int(math.Ceil(float64(len(text)) / 4.0))
}

func parsePricePerMillion(raw string) (float64, float64, bool) {
	matches := priceNumberPattern.FindAllStringSubmatch(raw, -1)
	if len(matches) < 2 {
		return 0, 0, false
	}
	input, inputOK := parseFloat(matches[0][1])
	output, outputOK := parseFloat(matches[1][1])
	return input, output, inputOK && outputOK
}

func parseFloat(value string) (float64, bool) {
	var parsed float64
	if _, err := fmt.Sscanf(value, "%f", &parsed); err != nil {
		return 0, false
	}
	return parsed, true
}
