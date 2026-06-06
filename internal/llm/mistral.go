package llm

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
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

	if resp.StatusCode != 200 {
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

	return &CompletionResult{
		Content:      result.Choices[0].Message.Content,
		InputTokens:  result.Usage.PromptTokens,
		OutputTokens: result.Usage.CompletionTokens,
		TotalTokens:  result.Usage.TotalTokens,
	}, nil
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
