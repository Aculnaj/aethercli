package api

import (
	"bufio"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
)

// Request is deliberately constrained to known API routes. It cannot forward
// credentials to arbitrary URLs or fall back from sandbox to provider routes.
func (c *Client) Request(ctx context.Context, method, path string, payload any, stream bool, out io.Writer) error {
	if strings.HasPrefix(path, "/sandbox/") {
		u, err := url.Parse(c.baseURL)
		if err != nil || u.Path != "/v1" {
			return &APIError{Code: "sandbox_base_url", Message: "Sandbox requires base-url ending exactly in /v1; no request was sent"}
		}
	}
	if !allowedPath(method, path) {
		return fmt.Errorf("unsupported API endpoint")
	}
	if !stream {
		var cancel context.CancelFunc
		ctx, cancel = withDefaultTimeout(ctx)
		defer cancel()
	}
	resp, err := c.doJSON(ctx, method, path, payload)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return parseAPIError(resp)
	}
	if strings.HasPrefix(path, "/sandbox/") && resp.Header.Get("X-Aether-Sandbox") != "true" {
		return &APIError{Code: "sandbox_unverified", Message: "Server did not confirm X-Aether-Sandbox: true; no fallback was attempted"}
	}
	if stream {
		if !strings.HasPrefix(strings.ToLower(resp.Header.Get("Content-Type")), "text/event-stream") {
			return &APIError{Code: "invalid_stream", Message: "Expected text/event-stream"}
		}
		return consumeSSE(resp.Body, func(_ string, _ string, frame string) error {
			if _, err := io.WriteString(out, frame); err != nil {
				return err
			}
			if f, ok := out.(interface{ Flush() error }); ok {
				return f.Flush()
			}
			return nil
		})
	}
	data, err := io.ReadAll(io.LimitReader(resp.Body, (32<<20)+1))
	if err != nil {
		return err
	}
	if len(data) > 32<<20 {
		return &APIError{Code: "response_too_large", Message: "API response exceeds 32 MiB"}
	}
	if !json.Valid(data) {
		return &APIError{Code: "invalid_response", Message: "Expected JSON response"}
	}
	_, err = fmt.Fprintln(out, string(data))
	return err
}

func allowedPath(method, path string) bool {
	if method == http.MethodGet {
		return path == "/models" || path == "/usage" || (path == "/usage?period=today" || path == "/usage?period=7" || path == "/usage?period=30" || path == "/usage?period=retained") || path == "/budget-alerts"
	}
	if method != http.MethodPost {
		return false
	}
	if strings.HasPrefix(path, "/sandbox/") {
		return path == "/sandbox/chat/completions" || path == "/sandbox/responses" || path == "/sandbox/messages"
	}
	return path == "/chat/completions" || path == "/responses" || path == "/messages" || path == "/embeddings" || path == "/images/generations"
}

// consumeSSE parses bounded events, preserves their SSE representation, reports
// in-band failures, and rejects truncated streams even after partial output.
func consumeSSE(body io.Reader, onEvent func(event, data, frame string) error) error {
	scanner := bufio.NewScanner(body)
	scanner.Buffer(make([]byte, 64*1024), 1<<20)
	var lines, data []string
	var event string
	size := 0
	terminal := false
	dispatch := func() error {
		if len(data) == 0 {
			lines = nil
			event = ""
			size = 0
			return nil
		}
		joined := strings.Join(data, "\n")
		frame := strings.Join(lines, "\n") + "\n\n"
		if joined != "[DONE]" {
			var item struct {
				Type     string          `json:"type"`
				Error    json.RawMessage `json:"error"`
				Response struct {
					Error json.RawMessage `json:"error"`
				} `json:"response"`
			}
			if err := json.Unmarshal([]byte(joined), &item); err != nil {
				return &APIError{Code: "invalid_stream", Message: "Malformed JSON stream event"}
			}
			if len(item.Error) > 0 && string(item.Error) != "null" || item.Type == "error" || item.Type == "response.failed" || item.Type == "response.incomplete" || event == "error" {
				var detail struct {
					Code    string `json:"code"`
					Type    string `json:"type"`
					Message string `json:"message"`
				}
				raw := item.Error
				if len(raw) == 0 || string(raw) == "null" {
					raw = item.Response.Error
				}
				if len(raw) == 0 || string(raw) == "null" {
					raw = json.RawMessage(joined)
				}
				_ = json.Unmarshal(raw, &detail)
				code := firstNonEmpty(detail.Code, detail.Type, "stream_error")
				return &APIError{Code: code, Message: firstNonEmpty(detail.Message, "Server reported a stream failure")}
			}
			terminal = item.Type == "response.completed" || item.Type == "message_stop" || event == "message_stop"
		} else {
			terminal = true
		}
		if err := onEvent(event, joined, frame); err != nil {
			return err
		}
		lines, data, event, size = nil, nil, "", 0
		return nil
	}
	for scanner.Scan() {
		line := scanner.Text()
		if line == "" {
			if err := dispatch(); err != nil {
				return err
			}
			if terminal {
				return nil
			}
			continue
		}
		size += len(line)
		if size > 1<<20 {
			return &APIError{Code: "invalid_stream", Message: "Stream event exceeds 1 MiB"}
		}
		lines = append(lines, line)
		if strings.HasPrefix(line, "data:") {
			data = append(data, strings.TrimPrefix(strings.TrimPrefix(line, "data:"), " "))
		}
		if strings.HasPrefix(line, "event:") {
			event = strings.TrimSpace(strings.TrimPrefix(line, "event:"))
		}
	}
	if err := scanner.Err(); err != nil {
		if err == bufio.ErrTooLong {
			return &APIError{Code: "invalid_stream", Message: "Stream line exceeds 1 MiB"}
		}
		return err
	}
	// Dispatch an event lacking a final blank line, but still require a terminal marker.
	if err := dispatch(); err != nil {
		return err
	}
	if terminal {
		return nil
	}
	return &APIError{Code: "stream_truncated", Message: "Stream ended without a completion marker; output may be partial"}
}
