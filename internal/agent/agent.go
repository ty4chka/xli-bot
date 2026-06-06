// internal/agent/agent.go (финальная версия со скиллами и MCP)
package agent

import (
	"context"
	"fmt"
	"strings"

	"github.com/oblachko/xli-bot/internal/llm"
	"github.com/oblachko/xli-bot/internal/mcp"
	"github.com/oblachko/xli-bot/internal/memory"
	"github.com/oblachko/xli-bot/internal/skills"
	"github.com/oblachko/xli-bot/internal/utils"
)

type Agent struct {
	LLM       llm.Client
	Memory    memory.Store
	Executor  *ToolExecutor
	Skills    skills.Registry
	MCP       *mcp.Client
	MaxSteps  int
}

type AgentResult struct {
	Answer        string
	InputTokens   int
	OutputTokens  int
	TotalTokens   int
	ThinkingNotes []string
	AgentLog      []string
}

func NewAgent(llmClient llm.Client, store memory.Store, executor *ToolExecutor, skillRegistry skills.Registry, mcpClient *mcp.Client) *Agent {
	return &Agent{
		LLM:      llmClient,
		Memory:   store,
		Executor: executor,
		Skills:   skillRegistry,
		MCP:      mcpClient,
		MaxSteps: 15,
	}
}

func (a *Agent) Run(ctx context.Context, chatID int64, task string) (*AgentResult, error) {
	result := &AgentResult{}
	a.Memory.SaveMessage(chatID, "user", task)

	// Подгружаем скиллы
	skillPrompt := a.Skills.BuildPrompt(task)

	// ReAct цикл
	for step := 0; step < a.MaxSteps; step++ {
		history, _ := a.Memory.LoadHistory(chatID, 50)
		messages := a.buildMessages(history, task, skillPrompt, step)

		response, err := a.LLM.Complete(ctx, messages, &llm.CompletionOpts{
			Model:       "mistral-large-latest",
			Temperature: 0.7,
			MaxTokens:   4000,
		})
		if err != nil {
			return nil, err
		}

		result.InputTokens += response.InputTokens
		result.OutputTokens += response.OutputTokens
		result.TotalTokens += response.TotalTokens

		calls := ParseToolCalls(response.Content)

		if len(calls) == 0 {
			result.Answer = response.Content
			break
		}

		for _, call := range calls {
			result.AgentLog = append(result.AgentLog, fmt.Sprintf("Step %d: %s", step+1, call.Tool))

			var output string
			var err error

			// MCP тулы — через MCP клиент
			if a.isMCPTool(call.Tool) {
				output, err = a.executeMCPTool(ctx, call)
			} else {
				// Встроенные тулы — через Executor
				output, err = a.Executor.Execute(ctx, chatID, 0, call)
			}

			if err != nil {
				output = fmt.Sprintf("Error: %v", err)
			}

			a.Memory.SaveMessage(chatID, "assistant", response.Content)
			a.Memory.SaveMessage(chatID, "user", fmt.Sprintf("Tool <%s> output:\n%s", call.Tool, output))
			a.Memory.SaveToolMemory(chatID, call.Tool, output)

			if call.Tool == "thinking.note" {
				if note, ok := call.Args["note"].(string); ok {
					result.ThinkingNotes = append(result.ThinkingNotes, note)
				}
			}
		}
	}

	if result.Answer != "" {
		a.Memory.SaveMessage(chatID, "assistant", result.Answer)
	}

	return result, nil
}

func (a *Agent) isMCPTool(toolName string) bool {
	// Проверяем есть ли тул в MCP
	tools := a.MCP.ListAllTools()
	for _, t := range tools {
		if t.Name == toolName {
			return true
		}
	}
	return false
}

func (a *Agent) executeMCPTool(ctx context.Context, call ToolCall) (string, error) {
	// Находим сервер которому принадлежит тул
	tools := a.MCP.ListAllTools()
	var serverName string
	for _, t := range tools {
		if t.Name == call.Tool {
			serverName = t.Server
			break
		}
	}

	if serverName == "" {
		return "", fmt.Errorf("MCP tool not found: %s", call.Tool)
	}

	return a.MCP.CallTool(ctx, serverName, call.Tool, call.Args)
}

func (a *Agent) buildMessages(history []memory.Message, task, skillPrompt string, step int) []llm.Message {
	var messages []llm.Message

	// System prompt
	systemContent := `You are XLI-Go Bot, an AI assistant with tool calling capabilities.

Available built-in tools:
` + "```tool_call" + `
{"tool":"thinking.note","args":{"note":"your thought"}}
` + "```" + `
` + "```tool_call" + `
{"tool":"terminal.run","args":{"cmd":"command"}}
` + "```" + `
` + "```tool_call" + `
{"tool":"file.read","args":{"path":"/path"}}
` + "```" + `
` + "```tool_call" + `
{"tool":"file.write","args":{"path":"/path","content":"data"}}
` + "```" + `
` + "```tool_call" + `
{"tool":"web.search","args":{"query":"search"}}
` + "```" + `
` + "```tool_call" + `
{"tool":"web.fetch","args":{"url":"https://..."}}
` + "```" + `
` + "```tool_call" + `
{"tool":"github.build","args":{"repo":"owner/repo"}}
` + "```" + `

Available MCP tools (auto-discovered):
`

	// Добавляем MCP тулы
	mcpTools := a.MCP.ListAllTools()
	for _, tool := range mcpTools {
		systemContent += fmt.Sprintf("- %s: %s\n", tool.Name, tool.Description)
	}

	systemContent += "\nRules:\n1. Use thinking.note to plan\n2. Use web.search for current info\n3. Respond normally when no tools needed\n4. Always use tool_call format for tools\n"

	// Добавляем скиллы
	if skillPrompt != "" {
		systemContent += "\n" + skillPrompt
	}

	messages = append(messages, llm.Message{
		Role:    "system",
		Content: systemContent,
	})

	// История
	for _, msg := range history {
		messages = append(messages, llm.Message{
			Role:    msg.Role,
			Content: msg.Content,
		})
	}

	return messages
}

func (a *Agent) SimpleAsk(ctx context.Context, task string) (*AgentResult, error) {
	response, err := a.LLM.Complete(ctx, []llm.Message{
		{Role: "user", Content: task},
	}, &llm.CompletionOpts{
		Model:       "mistral-large-latest",
		Temperature: 0.7,
		MaxTokens:   4000,
	})
	if err != nil {
		return nil, err
	}

	return &AgentResult{
		Answer:        response.Content,
		InputTokens:   response.InputTokens,
		OutputTokens:  response.OutputTokens,
		TotalTokens:   response.TotalTokens,
	}, nil
}
