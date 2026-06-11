package api

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"
)

type HTTPDoer interface {
	Do(req *http.Request) (*http.Response, error)
}

type ClientOptions struct {
	BaseURL    string
	APIKey     string
	HTTPClient HTTPDoer
}

type Client struct {
	baseURL    string
	apiKey     string
	httpClient HTTPDoer
}

type ChatRequest struct {
	Model       string
	Prompt      string
	Temperature *float64
	MaxTokens   *int
}

type ChatResponse struct {
	Model   string `json:"model,omitempty"`
	Content string `json:"content"`
}

type Model struct {
	ID                 string   `json:"id"`
	Object             string   `json:"object,omitempty"`
	OwnedBy            string   `json:"owned_by,omitempty"`
	Context            string   `json:"context,omitempty"`
	OurPrice           string   `json:"our_price,omitempty"`
	Endpoint           string   `json:"endpoint,omitempty"`
	SupportedEndpoints []string `json:"supported_endpoints,omitempty"`
}

type APIError struct {
	StatusCode int
	Code       string
	Message    string
}

func NewClient(opts ClientOptions) *Client {
	httpClient := opts.HTTPClient
	if httpClient == nil {
		httpClient = &http.Client{Timeout: 60 * time.Second}
	}
	return &Client{
		baseURL:    strings.TrimRight(opts.BaseURL, "/"),
		apiKey:     opts.APIKey,
		httpClient: httpClient,
	}
}

func (c *Client) Chat(ctx context.Context, req ChatRequest) (ChatResponse, error) {
	payload := chatPayloadFromRequest(req, false)
	resp, err := c.doJSON(ctx, http.MethodPost, "/chat/completions", payload)
	if err != nil {
		return ChatResponse{}, err
	}
	defer resp.Body.Close()

	if resp.StatusCode >= 400 {
		return ChatResponse{}, parseAPIError(resp)
	}

	var decoded struct {
		Model   string `json:"model"`
		Choices []struct {
			Message struct {
				Content string `json:"content"`
			} `json:"message"`
		} `json:"choices"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&decoded); err != nil {
		return ChatResponse{}, err
	}
	if len(decoded.Choices) == 0 || decoded.Choices[0].Message.Content == "" {
		return ChatResponse{}, fmt.Errorf("malformed API response: missing assistant content")
	}
	return ChatResponse{
		Model:   firstNonEmpty(decoded.Model, req.Model),
		Content: decoded.Choices[0].Message.Content,
	}, nil
}

func (c *Client) StreamChat(ctx context.Context, req ChatRequest, onDelta func(delta string) error) error {
	payload := chatPayloadFromRequest(req, true)
	resp, err := c.doJSON(ctx, http.MethodPost, "/chat/completions", payload)
	if err != nil {
		return err
	}
	defer resp.Body.Close()

	if resp.StatusCode >= 400 {
		return parseAPIError(resp)
	}
	return parseSSE(resp.Body, onDelta)
}

func (c *Client) Models(ctx context.Context) ([]Model, error) {
	resp, err := c.doJSON(ctx, http.MethodGet, "/models", nil)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()

	if resp.StatusCode >= 400 {
		return nil, parseAPIError(resp)
	}

	var decoded struct {
		Data []Model `json:"data"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&decoded); err != nil {
		return nil, err
	}
	return decoded.Data, nil
}

func FilterChatModels(models []Model) []Model {
	filtered := make([]Model, 0, len(models))
	for _, model := range models {
		if model.SupportsChatCompletions() {
			filtered = append(filtered, model)
		}
	}
	return filtered
}

func (m Model) SupportsChatCompletions() bool {
	if normalizeEndpoint(m.Endpoint) == "/v1/chat/completions" {
		return true
	}
	for _, endpoint := range m.SupportedEndpoints {
		if normalizeEndpoint(endpoint) == "/v1/chat/completions" {
			return true
		}
	}
	return false
}

func (e *APIError) Error() string {
	if e.Code != "" && e.Message != "" {
		return fmt.Sprintf("AetherAPI error %d (%s): %s", e.StatusCode, e.Code, e.Message)
	}
	if e.Message != "" {
		return fmt.Sprintf("AetherAPI error %d: %s", e.StatusCode, e.Message)
	}
	return fmt.Sprintf("AetherAPI error %d", e.StatusCode)
}

func (e *APIError) UserMessage() string {
	switch {
	case e.Code == "invalid_api_key" || e.StatusCode == http.StatusUnauthorized:
		return "invalid API key: run `aether setup` or set AETHER_API_KEY with a valid AetherAPI key"
	case e.Code == "insufficient_credits":
		return "insufficient credits: top up your AetherAPI account in the dashboard"
	case e.Code == "model_not_found":
		return "model not found: run `aether models` and choose a chat model"
	case e.Code == "rate_limit_exceeded" || e.StatusCode == http.StatusTooManyRequests:
		return "rate limit exceeded: wait and retry the request"
	default:
		return e.Error()
	}
}

func (c *Client) doJSON(ctx context.Context, method, path string, payload any) (*http.Response, error) {
	var body io.Reader
	if payload != nil {
		data, err := json.Marshal(payload)
		if err != nil {
			return nil, err
		}
		body = bytes.NewReader(data)
	}

	req, err := http.NewRequestWithContext(ctx, method, c.url(path), body)
	if err != nil {
		return nil, err
	}
	if payload != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	if c.apiKey != "" {
		req.Header.Set("Authorization", "Bearer "+c.apiKey)
	}
	return c.httpClient.Do(req)
}

func (c *Client) url(path string) string {
	return strings.TrimRight(c.baseURL, "/") + "/" + strings.TrimLeft(path, "/")
}

type chatPayload struct {
	Model       string        `json:"model"`
	Messages    []chatMessage `json:"messages"`
	Temperature *float64      `json:"temperature,omitempty"`
	MaxTokens   *int          `json:"max_tokens,omitempty"`
	Stream      bool          `json:"stream"`
}

type chatMessage struct {
	Role    string `json:"role"`
	Content string `json:"content"`
}

func chatPayloadFromRequest(req ChatRequest, stream bool) chatPayload {
	return chatPayload{
		Model: req.Model,
		Messages: []chatMessage{
			{Role: "user", Content: req.Prompt},
		},
		Temperature: req.Temperature,
		MaxTokens:   req.MaxTokens,
		Stream:      stream,
	}
}

func parseSSE(body io.Reader, onDelta func(delta string) error) error {
	scanner := bufio.NewScanner(body)
	scanner.Buffer(make([]byte, 0, 64*1024), 1024*1024)
	for scanner.Scan() {
		line := strings.TrimSpace(scanner.Text())
		if line == "" || strings.HasPrefix(line, ":") || !strings.HasPrefix(line, "data:") {
			continue
		}
		data := strings.TrimSpace(strings.TrimPrefix(line, "data:"))
		if data == "[DONE]" {
			return nil
		}

		var event struct {
			Choices []struct {
				Delta struct {
					Content string `json:"content"`
				} `json:"delta"`
			} `json:"choices"`
		}
		if err := json.Unmarshal([]byte(data), &event); err != nil {
			return fmt.Errorf("malformed stream event: %w", err)
		}
		for _, choice := range event.Choices {
			if choice.Delta.Content == "" {
				continue
			}
			if err := onDelta(choice.Delta.Content); err != nil {
				return err
			}
		}
	}
	return scanner.Err()
}

func parseAPIError(resp *http.Response) error {
	data, _ := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	apiErr := &APIError{StatusCode: resp.StatusCode}

	var decoded struct {
		Error struct {
			Code    string `json:"code"`
			Message string `json:"message"`
			Type    string `json:"type"`
		} `json:"error"`
	}
	if err := json.Unmarshal(data, &decoded); err == nil {
		apiErr.Code = decoded.Error.Code
		apiErr.Message = decoded.Error.Message
		if apiErr.Code == "" {
			apiErr.Code = decoded.Error.Type
		}
	}
	if apiErr.Message == "" {
		apiErr.Message = strings.TrimSpace(string(data))
	}
	return apiErr
}

func normalizeEndpoint(endpoint string) string {
	endpoint = strings.TrimSpace(endpoint)
	if endpoint == "/chat/completions" {
		return "/v1/chat/completions"
	}
	return endpoint
}

func firstNonEmpty(values ...string) string {
	for _, value := range values {
		if value != "" {
			return value
		}
	}
	return ""
}
