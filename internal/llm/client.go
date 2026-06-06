package llm

import "context"

type Client interface {
	Complete(ctx context.Context, messages []Message, opts *CompletionOpts) (*CompletionResult, error)
}

type Message struct {
	Role    string
	Content string
}

type CompletionOpts struct {
	Model       string
	Temperature float64
	MaxTokens   int
}

type CompletionResult struct {
	Content      string
	InputTokens  int
	OutputTokens int
	TotalTokens  int
}
