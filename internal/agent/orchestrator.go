package agent

import (
	"context"
	"fmt"
	"path/filepath"
	"strings"

	"github.com/oblachko/xli-bot/internal/llm"
	"github.com/oblachko/xli-bot/internal/mcp"
	"github.com/oblachko/xli-bot/internal/memory"
	"github.com/oblachko/xli-bot/internal/skills"
)

// AgentType — тип агента
type AgentType string

const (
	AgentGeneral AgentType = "general"
	AgentCoder   AgentType = "coder"
	AgentDebug   AgentType = "debug"
	AgentSearch  AgentType = "search"
	AgentBuild   AgentType = "build"
	AgentReview  AgentType = "review"
)

// SubAgent — агент под конкретную задачу
type SubAgent struct {
	Type         AgentType
	Name         string
	SystemPrompt string
	Skills       []skills.Skill
	Tools        []string // разрешённые тулзы
	MaxSteps     int
	LLM          llm.Client
	Memory       memory.Store
	Executor     *ToolExecutor
	MCP          *mcp.Client
}

// Orchestrator — выбирает агента под задачу
type Orchestrator struct {
	MainAgent *Agent
	LLM       llm.Client
}

// TaskAnalysis — анализ запроса
type TaskAnalysis struct {
	AgentType   AgentType
	Confidence  float64
	Reasoning   string
	NeedsSkills []string
	NeedsTools  []string
	Complexity  int // 1-10
}

func NewOrchestrator(mainAgent *Agent, llm llm.Client) *Orchestrator {
	return &Orchestrator{
		MainAgent: mainAgent,
		LLM:       llm,
	}
}

// AnalyzeTask — LLM анализирует запрос и выбирает агента
func (o *Orchestrator) AnalyzeTask(ctx context.Context, query string) (*TaskAnalysis, error) {
	prompt := fmt.Sprintf(`Analyze the user request and select the best agent.

Available agents:
- general: General questions, chat, explanations
- coder: Write code, scripts, algorithms, compile
- debug: Fix bugs, analyze errors, debugging
- search: Web search, research, current info
- build: Compile, build, CI/CD, GitHub Actions
- review: Code review, analysis, optimization

Request: "%s"

Respond in this exact format:
AGENT: <type>
CONFIDENCE: <0-100>
REASONING: <why>
NEEDS_SKILLS: <skill1,skill2,...>
NEEDS_TOOLS: <tool1,tool2,...>
COMPLEXITY: <1-10>`, query)

	response, err := o.LLM.Complete(ctx, []llm.Message{
		{Role: "user", Content: prompt},
	}, &llm.CompletionOpts{
		Model:       "mistral-large-latest",
		Temperature: 0.1,
		MaxTokens:   500,
	})
	if err != nil {
		return &TaskAnalysis{
			AgentType:  AgentGeneral,
			Confidence: 50,
			Reasoning:  "Fallback due to error",
			Complexity: 5,
		}, nil
	}

	return parseTaskAnalysis(response.Content), nil
}

func parseTaskAnalysis(text string) *TaskAnalysis {
	analysis := &TaskAnalysis{
		AgentType:  AgentGeneral,
		Confidence: 50,
		Complexity: 5,
	}

	lines := strings.Split(text, "\n")
	for _, line := range lines {
		line = strings.TrimSpace(line)
		if strings.HasPrefix(line, "AGENT:") {
			analysis.AgentType = AgentType(strings.TrimSpace(strings.TrimPrefix(line, "AGENT:")))
		}
		if strings.HasPrefix(line, "CONFIDENCE:") {
			fmt.Sscanf(line, "CONFIDENCE: %f", &analysis.Confidence)
		}
		if strings.HasPrefix(line, "REASONING:") {
			analysis.Reasoning = strings.TrimSpace(strings.TrimPrefix(line, "REASONING:"))
		}
		if strings.HasPrefix(line, "NEEDS_SKILLS:") {
			skillsStr := strings.TrimSpace(strings.TrimPrefix(line, "NEEDS_SKILLS:"))
			if skillsStr != "" {
				analysis.NeedsSkills = strings.Split(skillsStr, ",")
			}
		}
		if strings.HasPrefix(line, "NEEDS_TOOLS:") {
			toolsStr := strings.TrimSpace(strings.TrimPrefix(line, "NEEDS_TOOLS:"))
			if toolsStr != "" {
				analysis.NeedsTools = strings.Split(toolsStr, ",")
			}
		}
		if strings.HasPrefix(line, "COMPLEXITY:") {
			fmt.Sscanf(line, "COMPLEXITY: %d", &analysis.Complexity)
		}
	}

	return analysis
}

// CreateSubAgent — создаёт агента под задачу
func (o *Orchestrator) CreateSubAgent(analysis *TaskAnalysis, skillRegistry skills.Registry) *SubAgent {
	// Находим релевантные скиллы
	var relevantSkills []skills.Skill
	if hotLoader, ok := skillRegistry.(*skills.HotLoader); ok {
		matches := hotLoader.FindRelevant(strings.Join(analysis.NeedsSkills, " "), 5)
		for _, m := range matches {
			relevantSkills = append(relevantSkills, *m.Skill)
		}
	}

	// Определяем разрешённые тулзы (built-in + MCP)
	allowedTools := []string{
		"thinking.note", "terminal.run", "file.read", "file.write",
		"web.search", "web.fetch", "github.build", "sandbox.run",
		// MCP tools — ленивые, пробуем все
		"search_code", "analyze_traceback", "run_tests", "fix_test",
		"suggest_command", "fix_typo", "generate_complex_command",
		"list_dependencies", "check_vulnerabilities", "update_dependencies",
		"prompt_create", "prompt_get", "prompt_list", "prompt_evaluate",
		"blame_line", "code_ownership", "commit_history", "temporal_coupling",
		"analyze_complexity", "detect_long_methods",
		"dependency_graph", "circular_dependencies", "suggest_modules",
		"discover_tests",
	}

	switch analysis.AgentType {
	case AgentCoder:
		allowedTools = append(allowedTools, "github.build", "sandbox.run")
	case AgentBuild:
		allowedTools = []string{"thinking.note", "terminal.run", "file.read", "file.write", "github.build", "sandbox.run"}
	case AgentSearch:
		allowedTools = []string{"thinking.note", "web.search", "web.fetch", "search_code"}
	case AgentDebug:
		allowedTools = []string{"thinking.note", "terminal.run", "analyze_traceback", "run_tests", "fix_test"}
	}

	systemPrompt := buildSystemPrompt(analysis.AgentType, relevantSkills)

	return &SubAgent{
		Type:         analysis.AgentType,
		Name:         fmt.Sprintf("%s-agent", analysis.AgentType),
		SystemPrompt: systemPrompt,
		Skills:       relevantSkills,
		Tools:        allowedTools,
		MaxSteps:     15,
		LLM:          o.MainAgent.LLM,
		Memory:       o.MainAgent.Memory,
		Executor:     o.MainAgent.Executor,
		MCP:          o.MainAgent.MCP,
	}
}

func buildSystemPrompt(agentType AgentType, skills []skills.Skill) string {
	var sb strings.Builder

	switch agentType {
	case AgentCoder:
		sb.WriteString("You are an expert programmer. Write clean, efficient, well-documented code.\n")
		sb.WriteString("CRITICAL: ALWAYS use file.write tool to save code to files. NEVER write code directly in response text.\n")
		sb.WriteString("After writing file, use terminal.run to compile and test.\n")
		sb.WriteString("Use best practices and modern patterns.\n")
	case AgentDebug:
		sb.WriteString("You are a debugging expert. Analyze errors systematically.\n")
		sb.WriteString("Use terminal.run to check logs, files, and system state.\n")
		sb.WriteString("Use MCP debugger tools to analyze tracebacks.\n")
	case AgentSearch:
		sb.WriteString("You are a research assistant. Find accurate, up-to-date information.\n")
		sb.WriteString("Use web.search and web.fetch to verify facts.\n")
		sb.WriteString("Use MCP knowledge_base to search existing code.\n")
	case AgentBuild:
		sb.WriteString("You are a build engineer. Handle compilation, CI/CD, and deployment.\n")
		sb.WriteString("Use github.build for CI/CD and terminal.run for local builds.\n")
		sb.WriteString("Use sandbox.run for testing compiled binaries.\n")
	case AgentReview:
		sb.WriteString("You are a code reviewer. Analyze code for bugs, security, performance.\n")
		sb.WriteString("Provide specific, actionable feedback.\n")
		sb.WriteString("Use MCP refactor tools to detect complexity issues.\n")
	default:
		sb.WriteString("You are a helpful AI assistant.\n")
	}

	if len(skills) > 0 {
		sb.WriteString("\nRelevant skills:\n")
		for _, s := range skills {
			sb.WriteString(fmt.Sprintf("=== %s ===\n%s\n", s.Name, s.Content))
		}
	}

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

	sb.WriteString("\nRules:\n")
	sb.WriteString("1. Use thinking.note to plan your approach\n")
	sb.WriteString("2. Use tools when needed, respond directly when not\n")
	sb.WriteString("3. ALWAYS use ```tool_call format\n")
	sb.WriteString("4. After writing .go file, COMPILE it with terminal.run go build\n")
	sb.WriteString("5. For Python scripts, use terminal.run python3 or sandbox.run\n")
	sb.WriteString("6. MCP tools are called same way as built-in tools\n")
	sb.WriteString("7. You can use MULTIPLE tools in sequence — one tool call per message\n")

	return sb.String()
}

// Run — выполняет задачу через подходящего агента
func (s *SubAgent) Run(ctx context.Context, chatID int64, task string) (*AgentResult, error) {
	result := &AgentResult{}
	s.Memory.SaveMessage(chatID, "user", task)

	for step := 0; step < s.MaxSteps; step++ {
		history, _ := s.Memory.LoadHistory(chatID, 50)
		messages := s.buildMessages(history, task)

		response, err := s.LLM.Complete(ctx, messages, &llm.CompletionOpts{
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

		s.Memory.SaveMessage(chatID, "assistant", response.Content)

		for _, call := range calls {
			// Проверяем, разрешён ли тул
			if !s.isToolAllowed(call.Tool) {
				output := fmt.Sprintf("Tool %s not allowed for this agent type", call.Tool)
				s.Memory.SaveMessage(chatID, "user", output)
				continue
			}

			result.AgentLog = append(result.AgentLog, fmt.Sprintf("Step %d: %s", step+1, call.Tool))

			var output string
			var err error

			// Сначала пробуем MCP (ленивый auto-routing)
			if s.MCP != nil {
				output, err = s.MCP.CallToolAuto(ctx, call.Tool, call.Args)
				if err == nil {
					// MCP сработал
					s.Memory.SaveMessage(chatID, "user", fmt.Sprintf("Tool <%s>:\n%s", call.Tool, output))
					s.Memory.SaveToolMemory(chatID, call.Tool, output)
					if call.Tool == "thinking.note" {
						if note, ok := call.Args["note"].(string); ok {
							result.ThinkingNotes = append(result.ThinkingNotes, note)
						}
					}
					continue
				}
				// Если ошибка не "Unknown tool" — возвращаем её
				if !strings.Contains(err.Error(), "Unknown") && !strings.Contains(err.Error(), "not found") {
					output = fmt.Sprintf("MCP error: %v", err)
					s.Memory.SaveMessage(chatID, "user", fmt.Sprintf("Tool <%s>:\n%s", call.Tool, output))
					continue
				}
				// Иначе пробуем built-in
			}

			// Built-in tools
			output, err = s.Executor.Execute(ctx, chatID, 0, call)
			if err != nil {
				output = fmt.Sprintf("Error: %v", err)
			}

			s.Memory.SaveMessage(chatID, "user", fmt.Sprintf("Tool <%s>:\n%s", call.Tool, output))
			s.Memory.SaveToolMemory(chatID, call.Tool, output)

			if call.Tool == "thinking.note" {
				if note, ok := call.Args["note"].(string); ok {
					result.ThinkingNotes = append(result.ThinkingNotes, note)
				}
			}

			// Авто-компиляция для coder/build агентов
			if call.Tool == "file.write" && (s.Type == AgentCoder || s.Type == AgentBuild) {
				path, _ := call.Args["path"].(string)
				if strings.HasSuffix(path, ".go") {
					compileCall := ToolCall{
						Tool: "terminal.run",
						Args: map[string]interface{}{
							"cmd": fmt.Sprintf("cd %s && go build -o %s %s",
								filepath.Dir(path),
								strings.TrimSuffix(filepath.Base(path), ".go"),
								filepath.Base(path)),
						},
					}
					compileOutput, compileErr := s.Executor.Execute(ctx, chatID, 0, compileCall)
					if compileErr != nil {
						output += fmt.Sprintf("\nCompile error: %v", compileErr)
					} else {
						output += fmt.Sprintf("\nCompiled: %s", compileOutput)
					}
					s.Memory.SaveMessage(chatID, "user", fmt.Sprintf("Tool <terminal.run>:\n%s", output))
				}
			}
		}
	}

	if result.Answer != "" {
		s.Memory.SaveMessage(chatID, "assistant", result.Answer)
	}

	return result, nil
}

func (s *SubAgent) isToolAllowed(toolName string) bool {
	for _, t := range s.Tools {
		if t == toolName {
			return true
		}
	}
	return false
}

func (s *SubAgent) buildMessages(history []memory.Message, task string) []llm.Message {
	var messages []llm.Message

	messages = append(messages, llm.Message{
		Role:    "system",
		Content: s.SystemPrompt,
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
