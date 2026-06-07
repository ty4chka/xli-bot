package llm

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log"
	"net/http"
	"os"
	"strings"
	"time"
)

type MistralClient struct {
	apiKey   string
	provider string
	model    string
	client   *http.Client
}

func NewMistralClient(apiKey, provider string) Client {
	model := os.Getenv("LLM_MODEL")
	if model == "" {
		model = "mistral-large-latest"
	}

	return &MistralClient{
		apiKey:   apiKey,
		provider: provider,
		model:    model,
		client:   &http.Client{},
	}
}

func (c *MistralClient) Complete(ctx context.Context, messages []Message, opts *CompletionOpts) (*CompletionResult, error) {
	var lastErr error
	for attempt := 0; attempt < 3; attempt++ {
		if attempt > 0 {
			backoff := time.Duration(attempt) * 2 * time.Second
			log.Printf("[LLM] Retry %d after %v", attempt, backoff)
			select {
			case <-time.After(backoff):
			case <-ctx.Done():
				return nil, ctx.Err()
			}
		}

		result, err := c.completeOnce(ctx, messages, opts)
		if err == nil {
			return result, nil
		}
		lastErr = err
		if !isRateLimit(err) {
			return nil, err
		}
	}
	return nil, fmt.Errorf("rate limit after retries: %w", lastErr)
}

func (c *MistralClient) completeOnce(ctx context.Context, messages []Message, opts *CompletionOpts) (*CompletionResult, error) {
	baseURL := "https://api.mistral.ai/v1"
	if c.provider == "groq" {
		baseURL = "https://api.groq.com/openai/v1"
	} else if c.provider == "openrouter" {
		baseURL = "https://openrouter.ai/api/v1"
	}

	model := c.model
	if opts != nil && opts.Model != "" {
		model = opts.Model
	}

	temp := 0.7
	if opts != nil && opts.Temperature != 0 {
		temp = opts.Temperature
	}

	maxTokens := 4000
	if opts != nil && opts.MaxTokens != 0 {
		maxTokens = opts.MaxTokens
	}

	reqBody := map[string]interface{}{
		"model":       model,
		"messages":    convertMessages(messages),
		"temperature": temp,
		"max_tokens":  maxTokens,
	}

	jsonBody, err := json.Marshal(reqBody)
	if err != nil {
		return nil, err
	}

	log.Printf("[LLM] Request: model=%s, messages=%d, max_tokens=%d", model, len(messages), maxTokens)

	req, err := http.NewRequestWithContext(ctx, "POST", baseURL+"/chat/completions", bytes.NewReader(jsonBody))
	if err != nil {
		return nil, err
	}

	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Authorization", "Bearer "+c.apiKey)

	resp, err := c.client.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()

	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, err
	}

	if resp.StatusCode == 429 {
		log.Printf("[LLM] Rate limited")
		return nil, fmt.Errorf("API error 429: %s", string(body))
	}
	if resp.StatusCode != 200 {
		log.Printf("[LLM] API error %d: %s", resp.StatusCode, string(body))
		return nil, fmt.Errorf("API error %d: %s", resp.StatusCode, string(body))
	}

	var result struct {
		Choices []struct {
			Message struct {
				Content string `json:"content"`
			} `json:"message"`
		} `json:"choices"`
		Usage struct {
			PromptTokens     int `json:"prompt_tokens"`
			CompletionTokens int `json:"completion_tokens"`
			TotalTokens      int `json:"total_tokens"`
		} `json:"usage"`
	}

	if err := json.Unmarshal(body, &result); err != nil {
		return nil, err
	}

	if len(result.Choices) == 0 {
		return nil, fmt.Errorf("no choices in response")
	}

	log.Printf("[LLM] Response: tokens=%d in/%d out/%d total, content=%d chars",
		result.Usage.PromptTokens, result.Usage.CompletionTokens, result.Usage.TotalTokens,
		len(result.Choices[0].Message.Content))

	return &CompletionResult{
		Content:      result.Choices[0].Message.Content,
		InputTokens:  result.Usage.PromptTokens,
		OutputTokens: result.Usage.CompletionTokens,
		TotalTokens:  result.Usage.TotalTokens,
	}, nil
}

func isRateLimit(err error) bool {
	if err == nil {
		return false
	}
	return strings.Contains(err.Error(), "429") || strings.Contains(err.Error(), "rate_limit")
}

func convertMessages(msgs []Message) []map[string]string {
	var result []map[string]string
	for _, m := range msgs {
		result = append(result, map[string]string{
			"role":    m.Role,
			"content": m.Content,
		})
	}
	return result
}
