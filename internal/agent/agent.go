package agent

import (
	"context"
	"fmt"
	"log"
	"strings"

	"github.com/oblachko/xli-bot/internal/llm"
	"github.com/oblachko/xli-bot/internal/mcp"
	"github.com/oblachko/xli-bot/internal/memory"
	"github.com/oblachko/xli-bot/internal/skills"
)

type Agent struct {
	LLM          llm.Client
	Memory       memory.Store
	Executor     *ToolExecutor
	Skills       skills.Registry
	MCP          *mcp.Client
	MaxSteps     int
	Orchestrator *Orchestrator
	TierExec     *TierExecutor // NEW: tier router
}

type AgentResult struct {
	Answer        string
	InputTokens   int
	OutputTokens  int
	TotalTokens   int
	ThinkingNotes []string
	AgentLog      []string
	AgentType     string
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

func (a *Agent) SetOrchestrator(o *Orchestrator) {
	a.Orchestrator = o
}

func (a *Agent) SetTierExecutor(te *TierExecutor) {
	a.TierExec = te
}

func (a *Agent) Run(ctx context.Context, chatID int64, task string) (*AgentResult, error) {
	log.Printf("[AGENT] START task: chat=%d task=%q", chatID, task)

	if a.TierExec != nil {
		log.Printf("[AGENT] Using TierRouter")
		return a.TierExec.Run(ctx, chatID, task)
	}

	if a.Orchestrator != nil {
		log.Printf("[AGENT] Using orchestrator")
		analysis, err := a.Orchestrator.AnalyzeTask(ctx, task)
		if err == nil && analysis.Confidence > 60 {
			log.Printf("[AGENT] Orchestrator selected: type=%s confidence=%.0f", analysis.AgentType, analysis.Confidence)
			subAgent := a.Orchestrator.CreateSubAgent(analysis, a.Skills)
			result, err := subAgent.Run(ctx, chatID, task)
			if err == nil {
				result.AgentType = string(analysis.AgentType)
				log.Printf("[AGENT] SubAgent success")
				return result, nil
			}
			log.Printf("[AGENT] SubAgent failed: %v, fallback", err)
		}
	}

	return a.runLegacy(ctx, chatID, task)
}

func (a *Agent) runLegacy(ctx context.Context, chatID int64, task string) (*AgentResult, error) {
	result := &AgentResult{AgentType: "general"}
	a.Memory.SaveMessage(chatID, "user", task)

	skillPrompt := ""
	if a.Skills != nil {
		skillPrompt = a.Skills.BuildPromptRelevant(task, 5)
	}
	log.Printf("[AGENT] Skills: %d chars", len(skillPrompt))

	for step := 0; step < a.MaxSteps; step++ {
		log.Printf("[AGENT] Step %d/%d", step+1, a.MaxSteps)

		history, _ := a.Memory.LoadHistory(chatID, 50)
		messages := a.buildMessages(history, task, skillPrompt)
		log.Printf("[AGENT] Messages: %d", len(messages))

		response, err := a.LLM.Complete(ctx, messages, &llm.CompletionOpts{
			Model:       "mistral-large-latest",
			Temperature: 0.7,
			MaxTokens:   32000,
		})
		if err != nil {
			log.Printf("[AGENT] LLM error: %v", err)
			return nil, err
		}

		result.InputTokens += response.InputTokens
		result.OutputTokens += response.OutputTokens
		result.TotalTokens += response.TotalTokens

		calls := ParseToolCalls(response.Content)
		log.Printf("[AGENT] Tool calls: %d", len(calls))

		if len(calls) == 0 {
			result.Answer = response.Content
			log.Printf("[AGENT] Final answer: %d chars", len(result.Answer))
			break
		}

		a.Memory.SaveMessage(chatID, "assistant", response.Content)

		for _, call := range calls {
			log.Printf("[AGENT] Executing: %s", call.Tool)
			result.AgentLog = append(result.AgentLog, fmt.Sprintf("Step %d: %s", step+1, call.Tool))

			var output string
			var err error

			if a.MCP != nil {
				output, err = a.MCP.CallToolAuto(ctx, call.Tool, call.Args)
				if err == nil {
					a.Memory.SaveMessage(chatID, "user", fmt.Sprintf("Tool <%s>: %s", call.Tool, output))
					if call.Tool == "thinking.note" {
						if note, ok := call.Args["note"].(string); ok {
							result.ThinkingNotes = append(result.ThinkingNotes, note)
						}
					}
					continue
				}
				log.Printf("[AGENT] MCP failed: %v", err)
			}

			output, err = a.Executor.Execute(ctx, chatID, 0, call)
			if err != nil {
				output = fmt.Sprintf("Error: %v", err)
			}

			a.Memory.SaveMessage(chatID, "user", fmt.Sprintf("Tool <%s>: %s", call.Tool, output))
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

	log.Printf("[AGENT] Done: %d tokens", result.TotalTokens)
	return result, nil
}

func (a *Agent) buildMessages(history []memory.Message, task, skillPrompt string) []llm.Message {
	var messages []llm.Message

	var sb strings.Builder
	sb.WriteString("You are XLI-Go Bot, an AI assistant.\n\n")
	sb.WriteString("CRITICAL RULES:\n")
	sb.WriteString("1. Be CONCISE. 2-3 sentences max unless asked for details.\n")
	sb.WriteString("2. Use tools for code/files. NEVER put code in text response.\n")
	sb.WriteString("3. Use thinking.note to plan\n")
	sb.WriteString("4. Use web.search for current info\n")
	sb.WriteString("5. ALWAYS use ```tool_call format\n")
	sb.WriteString("6. After writing .go file, COMPILE with terminal.run go build\n")
	sb.WriteString("7. MCP tools are called SAME WAY as built-in tools\n")
	sb.WriteString("8. You can use MULTIPLE tools in sequence\n")
	sb.WriteString("9. ALWAYS use file.write for code, NEVER in response text\n")
	sb.WriteString("10. For Python scripts, use terminal.run python3 or sandbox.run\n")

	sb.WriteString("\nBuilt-in tools:\n")
	sb.WriteString("```tool_call\n")
	sb.WriteString(`{"tool":"thinking.note","args":{"note":"your thought"}}`)
	sb.WriteString("\n```\n")
	sb.WriteString("```tool_call\n")
	sb.WriteString(`{"tool":"terminal.run","args":{"cmd":"command"}}`)
	sb.WriteString("\n```\n")
	sb.WriteString("```tool_call\n")
	sb.WriteString(`{"tool":"file.read","args":{"path":"/path"}}`)
	sb.WriteString("\n```\n")
	sb.WriteString("```tool_call\n")
	sb.WriteString(`{"tool":"file.write","args":{"path":"/path","content":"data"}}`)
	sb.WriteString("\n```\n")
	sb.WriteString("```tool_call\n")
	sb.WriteString(`{"tool":"web.search","args":{"query":"search"}}`)
	sb.WriteString("\n```\n")
	sb.WriteString("```tool_call\n")
	sb.WriteString(`{"tool":"web.fetch","args":{"url":"https://..."}}`)
	sb.WriteString("\n```\n")
	sb.WriteString("```tool_call\n")
	sb.WriteString(`{"tool":"archive.create","args":{"source":"/path","name":"archive.zip"}}`)
	sb.WriteString("\n```\n")
	sb.WriteString("```tool_call\n")
	sb.WriteString(`{"tool":"sandbox.run","args":{"path":"/path/to/binary"}}`)
	sb.WriteString("\n```\n")

	sb.WriteString("\nMCP tools (lazy loaded, auto-routed):\n")
	sb.WriteString("- search_code, analyze_traceback, run_tests, fix_test\n")
	sb.WriteString("- suggest_command, fix_typo, generate_complex_command\n")
	sb.WriteString("- list_dependencies, check_vulnerabilities, update_dependencies\n")
	sb.WriteString("- prompt_create, prompt_get, prompt_list, prompt_evaluate\n")
	sb.WriteString("- blame_line, code_ownership, commit_history, temporal_coupling\n")
	sb.WriteString("- analyze_complexity, detect_long_methods\n")
	sb.WriteString("- dependency_graph, circular_dependencies, suggest_modules\n")
	sb.WriteString("- discover_tests\n")

	if skillPrompt != "" {
		sb.WriteString("\n")
		sb.WriteString(skillPrompt)
	}

	messages = append(messages, llm.Message{
		Role:    "system",
		Content: sb.String(),
	})

	var lastRole string
	for _, msg := range history {
		if msg.Role == lastRole || strings.TrimSpace(msg.Content) == "" {
			continue
		}
		messages = append(messages, llm.Message{
			Role:    msg.Role,
			Content: msg.Content,
		})
		lastRole = msg.Role
	}

	if lastRole == "assistant" {
		messages = append(messages, llm.Message{
			Role:    "user",
			Content: "Continue",
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
		MaxTokens:   32000,
	})
	if err != nil {
		return nil, err
	}
	return &AgentResult{
		Answer:       response.Content,
		InputTokens:  response.InputTokens,
		OutputTokens: response.OutputTokens,
		TotalTokens:  response.TotalTokens,
		AgentType:    "general",
	}, nil
}

func truncate(s string, maxLen int) string {
	if len(s) <= maxLen {
		return s
	}
	return s[:maxLen] + "..."
}
