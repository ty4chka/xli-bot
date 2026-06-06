package llm

import "context"

type Message struct {
	Role    string `json:"role"`
	Content string `json:"content"`
}

type CompletionOpts struct {
	Model           string
	Temperature     float64
	MaxTokens       int
	ReasoningEffort string
}

type CompletionResult struct {
	Content      string
	InputTokens  int
	OutputTokens int
	TotalTokens  int
}

type TokenUsage struct {
	InputTokens  int
	OutputTokens int
	TotalTokens  int
}

type Client interface {
	Complete(ctx context.Context, messages []Message, opts *CompletionOpts) (*CompletionResult, error)
	Stream(ctx context.Context, messages []Message, opts *CompletionOpts) (<-chan string, error)
}
