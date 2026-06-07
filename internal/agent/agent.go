package agent

import (
	"context"
	"fmt"
	"log"
	"path/filepath"
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
	Orchestrator = o
}

func (a *Agent) Run(ctx context.Context, chatID int64, task string) (*AgentResult, error) {
	log.Printf("[AGENT] Starting task: chat=%d, task=%q", chatID, task)

	// Если есть оркестратор — используем его
	if a.Orchestrator != nil {
		log.Printf("[AGENT] Using orchestrator")
		analysis, err := a.Orchestrator.AnalyzeTask(ctx, task)
		if err == nil && analysis.Confidence > 60 {
			log.Printf("[AGENT] Orchestrator selected: type=%s, confidence=%.0f", analysis.AgentType, analysis.Confidence)
			subAgent := a.Orchestrator.CreateSubAgent(analysis, a.Skills)
			result, err := subAgent.Run(ctx, chatID, task)
			if err == nil {
				result.AgentType = string(analysis.AgentType)
				log.Printf("[AGENT] SubAgent done: type=%s, tokens=%d", result.AgentType, result.TotalTokens)
				return result, nil
			}
			log.Printf("[AGENT] SubAgent failed: %v, falling back to main agent", err)
		} else {
			log.Printf("[AGENT] Orchestrator fallback: err=%v, confidence=%.0f", err, analysis.Confidence)
		}
	}

	// Fallback на основной агент
	result := &AgentResult{AgentType: "general"}
	a.Memory.SaveMessage(chatID, "user", task)

	// 5 скиллов без обрезки
	skillPrompt := a.Skills.BuildPromptRelevant(task, 5)
	log.Printf("[AGENT] Skills loaded: %d chars", len(skillPrompt))

	for step := 0; step < a.MaxSteps; step++ {
		log.Printf("[AGENT] Step %d/%d", step+1, a.MaxSteps)

		history, _ := a.Memory.LoadHistory(chatID, 50)
		messages := a.buildMessages(history, task, skillPrompt)
		log.Printf("[AGENT] Messages: %d, system=%d chars", len(messages), len(messages[0].Content))

		response, err := a.LLM.Complete(ctx, messages, &llm.CompletionOpts{
			Model:       "mistral-large-latest",
			Temperature: 0.7,
			MaxTokens:   32000,  // ФИКС: 32K для длинных ответов
		})
		if err != nil {
			log.Printf("[AGENT] LLM error: %v", err)
			return nil, err
		}

		log.Printf("[AGENT] LLM response: tokens=%d in/%d out/%d total, content=%d chars",
			response.InputTokens, response.OutputTokens, response.TotalTokens, len(response.Content))

		result.InputTokens += response.InputTokens
		result.OutputTokens += response.OutputTokens
		result.TotalTokens += response.TotalTokens

		calls := ParseToolCalls(response.Content)
		log.Printf("[AGENT] Tool calls found: %d", len(calls))

		if len(calls) == 0 {
			result.Answer = response.Content
			log.Printf("[AGENT] No tool calls, final answer: %d chars", len(result.Answer))
			break
		}

		a.Memory.SaveMessage(chatID, "assistant", response.Content)

		for _, call := range calls {
			log.Printf("[AGENT] Executing tool: %s", call.Tool)
			result.AgentLog = append(result.AgentLog, fmt.Sprintf("Step %d: %s", step+1, call.Tool))

			var output string
			var err error

			// Сначала пробуем MCP (ленивый auto-routing)
			if a.MCP != nil {
				output, err = a.MCP.CallToolAuto(ctx, call.Tool, call.Args)
				if err == nil {
					log.Printf("[AGENT] MCP tool success: %s, output=%d chars", call.Tool, len(output))
					a.Memory.SaveMessage(chatID, "user", fmt.Sprintf("Tool <%s>:\n%s", call.Tool, output))
					a.Memory.SaveToolMemory(chatID, call.Tool, output)
					if call.Tool == "thinking.note" {
						if note, ok := call.Args["note"].(string); ok {
							result.ThinkingNotes = append(result.ThinkingNotes, note)
						}
					}
					continue
				}
				// Если не "Unknown tool" — логируем и пробуем built-in
				if !strings.Contains(err.Error(), "Unknown") && !strings.Contains(err.Error(), "not found") {
					log.Printf("[AGENT] MCP error: %v", err)
				}
			}

			// Built-in tools
			output, err = a.Executor.Execute(ctx, chatID, 0, call)
			if err != nil {
				log.Printf("[AGENT] Tool error: %s: %v", call.Tool, err)
				output = fmt.Sprintf("Error: %v", err)
			} else {
				log.Printf("[AGENT] Tool output: %s: %d chars", call.Tool, len(output))
			}

			a.Memory.SaveMessage(chatID, "user", fmt.Sprintf("Tool <%s>:\n%s", call.Tool, output))
			a.Memory.SaveToolMemory(chatID, call.Tool, output)

			if call.Tool == "thinking.note" {
				if note, ok := call.Args["note"].(string); ok {
					result.ThinkingNotes = append(result.ThinkingNotes, note)
				}
			}

			// Авто-компиляция для .go файлов
			if call.Tool == "file.write" {
				path, _ := call.Args["path"].(string)
				if strings.HasSuffix(path, ".go") {
					log.Printf("[AGENT] Auto-compiling: %s", path)
					compileCall := ToolCall{
						Tool: "terminal.run",
						Args: map[string]interface{}{
							"cmd": fmt.Sprintf("cd %s && go build -o %s %s",
								filepath.Dir(path),
								strings.TrimSuffix(filepath.Base(path), ".go"),
								filepath.Base(path)),
						},
					}
					compileOutput, compileErr := a.Executor.Execute(ctx, chatID, 0, compileCall)
					if compileErr != nil {
						log.Printf("[AGENT] Compile error: %v", compileErr)
						output += fmt.Sprintf("\n\nCompile error: %v", compileErr)
					} else {
						log.Printf("[AGENT] Compiled successfully: %d chars", len(compileOutput))
						output += fmt.Sprintf("\n\nCompiled: %s", compileOutput)
					}
					a.Memory.SaveMessage(chatID, "user", fmt.Sprintf("Tool <terminal.run>:\n%s", output))
				}
			}
		}
	}

	if result.Answer != "" {
		a.Memory.SaveMessage(chatID, "assistant", result.Answer)
	}

	log.Printf("[AGENT] Done: type=%s, tokens=%d, answer=%d chars", result.AgentType, result.TotalTokens, len(result.Answer))
	return result, nil
}

func (a *Agent) buildMessages(history []memory.Message, task, skillPrompt string) []llm.Message {
	var messages []llm.Message

	var sb strings.Builder
	// ФИКС: краткий системный промпт
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
	sb.WriteString(`{"tool":"github.build","args":{"lang":"go","path":"/path/to/file.go"}}`)
	sb.WriteString("\n```\n")
	sb.WriteString("```tool_call\n")
	sb.WriteString(`{"tool":"sandbox.run","args":{"path":"/path/to/binary"}}`)
	sb.WriteString("\n```\n")

	sb.WriteString("\nMCP tools (lazy loaded, auto-routed):\n")
	sb.WriteString("- search_code: Search code in knowledge base\n")
	sb.WriteString("- analyze_traceback: Debug errors\n")
	sb.WriteString("- run_tests, fix_test: Auto test code\n")
	sb.WriteString("- suggest_command, fix_typo, generate_complex_command: Shell helper\n")
	sb.WriteString("- list_dependencies, check_vulnerabilities, update_dependencies: Package monitor\n")
	sb.WriteString("- prompt_create, prompt_get, prompt_list, prompt_evaluate: Prompt management\n")
	sb.WriteString("- blame_line, code_ownership, commit_history, temporal_coupling: Git analysis\n")
	sb.WriteString("- analyze_complexity, detect_long_methods: Code complexity\n")
	sb.WriteString("- dependency_graph, circular_dependencies, suggest_modules: Architecture\n")
	sb.WriteString("- discover_tests: Test discovery\n")

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
