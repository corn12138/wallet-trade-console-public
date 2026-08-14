package ai

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

// The service tests use a fake Client. These tests instead exercise the real
// DeepSeek transport against an in-process server, pinning the wire contract a
// fake cannot observe without spending tokens or touching the network.
type capturedDeepSeekRequest struct {
	Method           string
	Path             string
	Authorization    string
	ContentType      string
	AnthropicVersion string
	XAPIKey          string
	RawBody          string
	Body             map[string]any
}

func captureDeepSeekAPI(t *testing.T, status int, responseBody string) (*httptest.Server, *capturedDeepSeekRequest) {
	t.Helper()
	captured := &capturedDeepSeekRequest{}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		captured.Method = r.Method
		captured.Path = r.URL.Path
		captured.Authorization = r.Header.Get("Authorization")
		captured.ContentType = r.Header.Get("Content-Type")
		captured.AnthropicVersion = r.Header.Get("anthropic-version")
		captured.XAPIKey = r.Header.Get("x-api-key")
		var raw json.RawMessage
		if err := json.NewDecoder(r.Body).Decode(&raw); err != nil {
			t.Errorf("decode request: %v", err)
		} else {
			captured.RawBody = string(raw)
			if err := json.Unmarshal(raw, &captured.Body); err != nil {
				t.Errorf("decode request object: %v", err)
			}
		}
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(status)
		_, _ = w.Write([]byte(responseBody))
	}))
	t.Cleanup(srv.Close)
	return srv, captured
}

const goodExplanationJSON = `{"headline":"h","whatHappens":["a"],"watchOut":[],"locale":"en"}`

func deepSeekResponse(finishReason string, content any) string {
	raw, _ := json.Marshal(map[string]any{
		"id":     "chatcmpl_1",
		"object": "chat.completion",
		"model":  "deepseek-v4-flash",
		"choices": []any{map[string]any{
			"index":         0,
			"finish_reason": finishReason,
			"message": map[string]any{
				"role":              "assistant",
				"content":           content,
				"reasoning_content": nil,
			},
		}},
	})
	return string(raw)
}

func testDeepSeekClient(t *testing.T, srv *httptest.Server, cfg Config) Client {
	t.Helper()
	cfg.BaseURL = srv.URL
	if cfg.APIKey == "" {
		cfg.APIKey = "sk-test"
	}
	c, err := NewDeepSeekClient(cfg)
	if err != nil {
		t.Fatalf("NewDeepSeekClient: %v", err)
	}
	return c
}

func TestDeepSeekRequestShape(t *testing.T) {
	srv, captured := captureDeepSeekAPI(t, http.StatusOK, deepSeekResponse("stop", goodExplanationJSON))
	c := testDeepSeekClient(t, srv, Config{Model: "deepseek-v4-pro", MaxTokens: 2048})

	if _, err := c.Explain(context.Background(), SystemPrompt, "user prompt"); err != nil {
		t.Fatalf("Explain: %v", err)
	}
	if captured.Method != http.MethodPost || captured.Path != "/chat/completions" {
		t.Errorf("request = %s %s", captured.Method, captured.Path)
	}
	if captured.Authorization != "Bearer sk-test" {
		t.Errorf("Authorization = %q", captured.Authorization)
	}
	if captured.ContentType != "application/json" {
		t.Errorf("Content-Type = %q", captured.ContentType)
	}
	if captured.AnthropicVersion != "" || captured.XAPIKey != "" {
		t.Errorf("Anthropic headers must not be sent: version=%q x-api-key=%q", captured.AnthropicVersion, captured.XAPIKey)
	}
	if strings.Contains(captured.RawBody, "sk-test") {
		t.Error("bearer credential leaked into the JSON body")
	}

	body := captured.Body
	if body["model"] != "deepseek-v4-pro" || body["max_tokens"] != float64(2048) {
		t.Errorf("model/max_tokens = %v/%v", body["model"], body["max_tokens"])
	}
	if body["temperature"] != float64(0) || body["stream"] != false {
		t.Errorf("temperature/stream = %v/%v", body["temperature"], body["stream"])
	}
	thinking, ok := body["thinking"].(map[string]any)
	if !ok || thinking["type"] != "disabled" {
		t.Errorf("thinking = %v, want {type: disabled}", body["thinking"])
	}
	format, ok := body["response_format"].(map[string]any)
	if !ok || format["type"] != "json_object" {
		t.Errorf("response_format = %v, want {type: json_object}", body["response_format"])
	}

	messages, ok := body["messages"].([]any)
	if !ok || len(messages) != 2 {
		t.Fatalf("messages = %v", body["messages"])
	}
	system := messages[0].(map[string]any)
	user := messages[1].(map[string]any)
	if system["role"] != "system" || user["role"] != "user" || user["content"] != "user prompt" {
		t.Errorf("messages = %v", messages)
	}
	systemText, _ := system["content"].(string)
	if !strings.Contains(strings.ToLower(systemText), "valid json") || !strings.Contains(systemText, `"headline"`) {
		t.Error("system prompt must request JSON and include the output shape")
	}
}

func TestDeepSeekExplainParsesJSONResponse(t *testing.T) {
	srv, _ := captureDeepSeekAPI(t, http.StatusOK, deepSeekResponse("stop", goodExplanationJSON))
	exp, err := testDeepSeekClient(t, srv, Config{}).Explain(context.Background(), SystemPrompt, "u")
	if err != nil {
		t.Fatalf("Explain: %v", err)
	}
	if exp.Headline != "h" || len(exp.WhatHappens) != 1 || exp.Locale != "en" {
		t.Errorf("explanation = %+v", exp)
	}
}

// Every unusable provider response becomes "no explanation"; Service then
// returns the deterministic static review instead of trusting partial prose.
func TestDeepSeekExplainRejectsUnusableResponses(t *testing.T) {
	cases := map[string]string{
		"truncated":              deepSeekResponse("length", `{"headline":"h"}`),
		"content filtered":       deepSeekResponse("content_filter", goodExplanationJSON),
		"unexpected tool call":   deepSeekResponse("tool_calls", goodExplanationJSON),
		"provider interrupted":   deepSeekResponse("insufficient_system_resource", goodExplanationJSON),
		"no choices":             `{"choices":[]}`,
		"null content":           deepSeekResponse("stop", nil),
		"empty content":          deepSeekResponse("stop", "  "),
		"not json":               deepSeekResponse("stop", "sure! here you go"),
		"unknown field":          deepSeekResponse("stop", `{"headline":"h","whatHappens":["a"],"watchOut":[],"locale":"en","verdict":"approved"}`),
		"missing required field": deepSeekResponse("stop", `{"headline":"h","whatHappens":["a"],"locale":"en"}`),
		"trailing json value":    deepSeekResponse("stop", goodExplanationJSON+` {}`),
		"invalid envelope":       `{not-json`,
	}
	for name, response := range cases {
		t.Run(name, func(t *testing.T) {
			srv, _ := captureDeepSeekAPI(t, http.StatusOK, response)
			if _, err := testDeepSeekClient(t, srv, Config{}).Explain(context.Background(), SystemPrompt, "u"); err == nil {
				t.Error("want an error so the service falls back to static")
			}
		})
	}
}

func TestDeepSeekExplainHonorsTimeout(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		select {
		case <-time.After(2 * time.Second):
			_, _ = w.Write([]byte(deepSeekResponse("stop", goodExplanationJSON)))
		case <-r.Context().Done():
			return
		}
	}))
	t.Cleanup(srv.Close)

	start := time.Now()
	_, err := testDeepSeekClient(t, srv, Config{Timeout: 150 * time.Millisecond}).
		Explain(context.Background(), SystemPrompt, "u")
	if err == nil {
		t.Fatal("want a timeout error")
	}
	if elapsed := time.Since(start); elapsed > time.Second {
		t.Errorf("took %s, timeout was not honored", elapsed)
	}
}

func TestDeepSeekExplainSurfacesHTTPErrorWithoutLeakingKey(t *testing.T) {
	response := `{"error":{"type":"rate_limit_error","message":"slow down; reflected sk-test"}}`
	srv, _ := captureDeepSeekAPI(t, http.StatusTooManyRequests, response)

	_, err := testDeepSeekClient(t, srv, Config{}).Explain(context.Background(), SystemPrompt, "u")
	if err == nil {
		t.Fatal("want an error on 429")
	}
	message := err.Error()
	if !strings.Contains(message, "429") {
		t.Errorf("error = %q", message)
	}
	if strings.Contains(message, "sk-test") || strings.Contains(message, "slow down") {
		t.Errorf("error leaked provider-controlled content: %q", message)
	}
}

func TestDeepSeekExplainDoesNotFollowRedirects(t *testing.T) {
	redirectTargetCalled := false
	target := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		redirectTargetCalled = true
		w.WriteHeader(http.StatusOK)
	}))
	t.Cleanup(target.Close)

	source := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Redirect(w, r, target.URL, http.StatusTemporaryRedirect)
	}))
	t.Cleanup(source.Close)

	_, err := testDeepSeekClient(t, source, Config{}).Explain(context.Background(), SystemPrompt, "u")
	if err == nil || !strings.Contains(err.Error(), "307") {
		t.Errorf("err = %v, want the redirect surfaced as an HTTP error", err)
	}
	if redirectTargetCalled {
		t.Error("provider redirect was followed; bearer credentials could leave the configured endpoint")
	}
}

func TestDeepSeekEndpointPreservesBasePath(t *testing.T) {
	got, err := deepSeekEndpoint("https://example.test/v1/")
	if err != nil {
		t.Fatalf("deepSeekEndpoint: %v", err)
	}
	if got != "https://example.test/v1/chat/completions" {
		t.Errorf("endpoint = %q", got)
	}
}
