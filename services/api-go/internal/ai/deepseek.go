package ai

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"strings"
	"time"
)

// DeepSeek is isolated in this transport file. The rest of the package works
// against Client, so switching providers never gives the model a path back into
// the deterministic tx-review decision.
type deepSeekClient struct {
	httpClient *http.Client
	endpoint   string
	apiKey     string
	model      string
	maxTokens  int64
	timeout    time.Duration
}

type deepSeekMessage struct {
	Role    string `json:"role"`
	Content string `json:"content"`
}

type deepSeekChatRequest struct {
	Model          string                 `json:"model"`
	Messages       []deepSeekMessage      `json:"messages"`
	Thinking       deepSeekThinking       `json:"thinking"`
	ResponseFormat deepSeekResponseFormat `json:"response_format"`
	MaxTokens      int64                  `json:"max_tokens"`
	Temperature    float64                `json:"temperature"`
	Stream         bool                   `json:"stream"`
}

type deepSeekThinking struct {
	Type string `json:"type"`
}

type deepSeekResponseFormat struct {
	Type string `json:"type"`
}

type deepSeekChatResponse struct {
	Choices []struct {
		FinishReason string `json:"finish_reason"`
		Message      struct {
			Content *string `json:"content"`
		} `json:"message"`
	} `json:"choices"`
}

type explanationJSON struct {
	Headline    *string   `json:"headline"`
	WhatHappens *[]string `json:"whatHappens"`
	WatchOut    *[]string `json:"watchOut"`
	Locale      *string   `json:"locale"`
}

const (
	deepSeekJSONResponseType = "json_object"
	deepSeekNonThinkingType  = "disabled"
	maxProviderResponseBytes = int64(1 << 20)
)

// NewDeepSeekClient constructs the OpenAI-compatible DeepSeek transport. A
// missing key produces no half-configured client: the caller wires nil and the
// explanation service deliberately stays on its complete static path.
func NewDeepSeekClient(cfg Config) (Client, error) {
	apiKey := strings.TrimSpace(cfg.APIKey)
	if apiKey == "" {
		return nil, ErrNotConfigured
	}

	model := strings.TrimSpace(cfg.Model)
	if model == "" {
		model = DefaultModel
	}
	if _, ok := supportedModels[model]; !ok {
		return nil, fmt.Errorf("%w %q; expected deepseek-v4-flash or deepseek-v4-pro", ErrUnsupportedModel, model)
	}
	maxTokens := cfg.MaxTokens
	if maxTokens <= 0 {
		maxTokens = DefaultMaxTokens
	}
	timeout := cfg.Timeout
	if timeout <= 0 {
		timeout = DefaultTimeout
	}
	endpoint, err := deepSeekEndpoint(cfg.BaseURL)
	if err != nil {
		return nil, err
	}

	return &deepSeekClient{
		// Provider redirects are not part of the chat-completions contract. Refuse
		// them so an HTTPS endpoint cannot redirect the bearer key to another host
		// or downgrade the request to plaintext.
		httpClient: &http.Client{CheckRedirect: func(_ *http.Request, _ []*http.Request) error {
			return http.ErrUseLastResponse
		}},
		endpoint:  endpoint,
		apiKey:    apiKey,
		model:     model,
		maxTokens: maxTokens,
		timeout:   timeout,
	}, nil
}

func (c *deepSeekClient) Model() string { return c.model }

func (c *deepSeekClient) Explain(ctx context.Context, systemPrompt, userPrompt string) (Explanation, error) {
	ctx, cancel := context.WithTimeout(ctx, c.timeout)
	defer cancel()

	// V4 defaults to thinking. This task only rephrases a verdict already made by
	// deterministic rules, so reasoning would add latency/cost and consume the
	// response budget while the user is waiting to sign.
	payload := deepSeekChatRequest{
		Model: c.model,
		Messages: []deepSeekMessage{
			{Role: "system", Content: systemPrompt},
			{Role: "user", Content: userPrompt},
		},
		Thinking:       deepSeekThinking{Type: deepSeekNonThinkingType},
		ResponseFormat: deepSeekResponseFormat{Type: deepSeekJSONResponseType},
		MaxTokens:      c.maxTokens,
		Temperature:    0,
		Stream:         false,
	}
	body, err := json.Marshal(payload)
	if err != nil {
		return Explanation{}, fmt.Errorf("ai: encode DeepSeek request: %w", err)
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, c.endpoint, bytes.NewReader(body))
	if err != nil {
		return Explanation{}, fmt.Errorf("ai: create DeepSeek request: %w", err)
	}
	req.Header.Set("Authorization", "Bearer "+c.apiKey)
	req.Header.Set("Content-Type", "application/json")

	resp, err := c.httpClient.Do(req)
	if err != nil {
		return Explanation{}, fmt.Errorf("ai: DeepSeek chat completion: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode < http.StatusOK || resp.StatusCode >= http.StatusMultipleChoices {
		return Explanation{}, deepSeekHTTPError(resp)
	}
	raw, err := readProviderBody(resp.Body, maxProviderResponseBytes)
	if err != nil {
		return Explanation{}, fmt.Errorf("ai: read DeepSeek response: %w", err)
	}
	var completion deepSeekChatResponse
	if err := json.Unmarshal(raw, &completion); err != nil {
		return Explanation{}, fmt.Errorf("ai: decode DeepSeek response: %w", err)
	}
	if len(completion.Choices) == 0 {
		return Explanation{}, fmt.Errorf("ai: DeepSeek response contained no choices")
	}
	choice := completion.Choices[0]
	// JSON from any other finish reason may be truncated, filtered, an unexpected
	// tool call, or interrupted by provider capacity. None is partially usable.
	if choice.FinishReason != "stop" {
		return Explanation{}, fmt.Errorf("ai: DeepSeek response finish_reason=%q", choice.FinishReason)
	}
	if choice.Message.Content == nil || strings.TrimSpace(*choice.Message.Content) == "" {
		// DeepSeek documents occasional empty content in JSON mode. Treat it as an
		// unavailable explanation so the existing static result remains authoritative.
		return Explanation{}, fmt.Errorf("ai: DeepSeek response contained no content")
	}

	exp, err := decodeExplanationJSON(*choice.Message.Content)
	if err != nil {
		return Explanation{}, fmt.Errorf("ai: decode explanation: %w", err)
	}
	return exp, nil
}

func deepSeekEndpoint(rawBaseURL string) (string, error) {
	baseURL := strings.TrimSpace(rawBaseURL)
	if baseURL == "" {
		baseURL = DefaultBaseURL
	}
	u, err := url.Parse(baseURL)
	if err != nil {
		return "", fmt.Errorf("ai: invalid DEEPSEEK_BASE_URL: %w", err)
	}
	if (u.Scheme != "http" && u.Scheme != "https") || u.Host == "" || u.User != nil || u.RawQuery != "" || u.Fragment != "" {
		return "", fmt.Errorf("ai: invalid DEEPSEEK_BASE_URL: expected an http(s) base URL without credentials, query, or fragment")
	}
	// The bearer key and server-generated review metadata must never travel over
	// plaintext. HTTP remains available only for loopback httptest/dev servers.
	if u.Scheme == "http" && !isLoopbackHost(u.Hostname()) {
		return "", fmt.Errorf("ai: invalid DEEPSEEK_BASE_URL: HTTPS is required outside loopback")
	}
	u.Path = strings.TrimRight(u.Path, "/") + "/chat/completions"
	u.RawPath = ""
	return u.String(), nil
}

func isLoopbackHost(host string) bool {
	if strings.EqualFold(host, "localhost") {
		return true
	}
	ip := net.ParseIP(host)
	return ip != nil && ip.IsLoopback()
}

func readProviderBody(r io.Reader, limit int64) ([]byte, error) {
	raw, err := io.ReadAll(io.LimitReader(r, limit+1))
	if err != nil {
		return nil, err
	}
	if int64(len(raw)) > limit {
		return nil, fmt.Errorf("response exceeded %d bytes", limit)
	}
	return raw, nil
}

func deepSeekHTTPError(resp *http.Response) error {
	// Provider error envelopes are not a stable contract and may echo request
	// content. Surface only the status so Service warn logs never duplicate the
	// user's pre-sign metadata or a reflected credential.
	return fmt.Errorf("ai: DeepSeek chat completion returned HTTP %d", resp.StatusCode)
}

// decodeExplanationJSON is intentionally stricter than provider JSON mode,
// which guarantees valid JSON but not this product's exact fields. Unknown,
// missing, null, or trailing values are rejected before domain validation.
func decodeExplanationJSON(content string) (Explanation, error) {
	dec := json.NewDecoder(strings.NewReader(content))
	dec.DisallowUnknownFields()
	var wire explanationJSON
	if err := dec.Decode(&wire); err != nil {
		return Explanation{}, err
	}
	if err := ensureJSONEOF(dec); err != nil {
		return Explanation{}, err
	}
	if wire.Headline == nil || wire.WhatHappens == nil || wire.WatchOut == nil || wire.Locale == nil {
		return Explanation{}, fmt.Errorf("response must contain headline, whatHappens, watchOut, and locale")
	}
	return Explanation{
		Headline:    *wire.Headline,
		WhatHappens: *wire.WhatHappens,
		WatchOut:    *wire.WatchOut,
		Locale:      *wire.Locale,
	}, nil
}

func ensureJSONEOF(dec *json.Decoder) error {
	var trailing any
	if err := dec.Decode(&trailing); err != io.EOF {
		if err == nil {
			return fmt.Errorf("response contained more than one JSON value")
		}
		return err
	}
	return nil
}
