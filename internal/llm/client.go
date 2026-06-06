package llm

import "context"

// Message represents a chat message
type Message struct {
	Role    string `json:"role"`    // system, user, assistant
	Content string `json:"content"`
}

// CompletionOpts contains options for completion requests
type CompletionOpts struct {
	Model           string
	Temperature     float64
	MaxTokens       int
	ReasoningEffort string // off, low, medium, high
}

// CompletionResult holds the response from the LLM
type CompletionResult struct {
	Content      string
	InputTokens  int
	OutputTokens int
	TotalTokens  int
}

// TokenUsage holds token statistics
type TokenUsage struct {
	InputTokens  int
	OutputTokens int
	TotalTokens  int
}

// Client is the interface for LLM providers
type Client interface {
	// Complete sends messages and returns the completion
	Complete(ctx context.Context, messages []Message, opts *CompletionOpts) (*CompletionResult, error)

	// Stream sends messages and returns a channel of tokens
	Stream(ctx context.Context, messages []Message, opts *CompletionOpts) (<-chan string, error)
}
