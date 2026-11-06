package summarize

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"
)

type Client struct {
	BaseURL string
	APIKey  string
	Model   string
	Timeout time.Duration
	HTTP    *http.Client
}

func NewClient(baseURL, apiKey, model string, timeout time.Duration) *Client {
	if timeout <= 0 {
		timeout = 30 * time.Second
	}
	return &Client{BaseURL: strings.TrimRight(baseURL, "/"), APIKey: apiKey, Model: model, Timeout: timeout}
}

func (c *Client) Summarize(ctx context.Context, prompt string) (string, error) {
	body := map[string]any{
		"model": c.Model,
		"messages": []map[string]string{
			{"role": "system", "content": "你是记忆整理助手，只输出简洁中文摘要，不编造。"},
			{"role": "user", "content": prompt},
		},
		"temperature": 0.2,
	}
	raw, err := json.Marshal(body)
	if err != nil {
		return "", err
	}
	endpoint := c.BaseURL + "/chat/completions"
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, endpoint, bytes.NewReader(raw))
	if err != nil {
		return "", err
	}
	req.Header.Set("Content-Type", "application/json")
	if c.APIKey != "" {
		req.Header.Set("Authorization", "Bearer "+c.APIKey)
	}
	client := c.HTTP
	if client == nil {
		client = &http.Client{Timeout: c.Timeout}
	}
	resp, err := client.Do(req)
	if err != nil {
		return "", err
	}
	defer resp.Body.Close()
	payload, err := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	if err != nil {
		return "", err
	}
	if resp.StatusCode/100 != 2 {
		return "", fmt.Errorf("llm %s: %s", resp.Status, truncateRunes(string(payload), 500))
	}
	var out struct {
		Choices []struct {
			Message struct {
				Content string `json:"content"`
			} `json:"message"`
		} `json:"choices"`
	}
	if err := json.Unmarshal(payload, &out); err != nil {
		return "", err
	}
	if len(out.Choices) == 0 || strings.TrimSpace(out.Choices[0].Message.Content) == "" {
		return "", fmt.Errorf("llm returned empty summary")
	}
	return strings.TrimSpace(out.Choices[0].Message.Content), nil
}

func truncateRunes(s string, n int) string {
	r := []rune(s)
	if len(r) <= n {
		return s
	}
	return string(r[:n]) + "…"
}
