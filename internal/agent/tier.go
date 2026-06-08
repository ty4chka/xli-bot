package agent

import (
	"context"
	"fmt"
	"log"
	"strings"
	"time"

	"github.com/oblachko/xli-bot/internal/llm"
	"github.com/oblachko/xli-bot/internal/memory"
)

// ExecutionTier — уровень сложности выполнения
type ExecutionTier int

const (
	Tier1Direct    ExecutionTier = 1 // Приветствия, вопросы, объяснения — 1 LLM вызов
	Tier2SingleAgent ExecutionTier = 2 // "Напиши скрипт", "Найди доки" — 1 агент, 1-4 шага
	Tier3MultiAgent  ExecutionTier = 3 // Проект с БД, компиляция, архив — мультиагент
)

// TierResult — результат анализа tier
type TierResult struct {
	Tier        ExecutionTier
	Confidence  float64
	Reasoning   string
	MaxSteps    int
	Timeout     time.Duration
	NeedsTools  []string
	IsSimple    bool // true = "простой" запрос → 1 файл, минимум шагов
}

// TierRouter — LLM-based анализатор сложности запроса
type TierRouter struct {
	LLM llm.Client
}

func NewTierRouter(llm llm.Client) *TierRouter {
	return &TierRouter{LLM: llm}
}

// Analyze — LLM анализирует запрос и выбирает tier
func (tr *TierRouter) Analyze(ctx context.Context, query string) (*TierResult, error) {
	prompt := fmt.Sprintf(`Analyze this user request and classify its complexity.

Request: "%s"

Rules for classification:
- "simple", "простой", "easy" in request -> ALWAYS Tier 1, max 1 file, <50 lines
- Greetings, questions, explanations -> Tier 1 (1 LLM call, no tools)
- "Write script", "find docs", "translate" -> Tier 2 (1 agent, 1-4 steps)
- "Project", "compile", "test", "archive", "DB", "API", "deploy" -> Tier 3 (multi-agent, up to 15 steps)

Respond EXACTLY in this format:
TIER: <1|2|3>
CONFIDENCE: <0-100>
REASONING: <brief explanation>
MAX_STEPS: <1-15>
TIMEOUT_SEC: <30-300>
IS_SIMPLE: <true|false>
NEEDS_TOOLS: <tool1,tool2,... or none>`, query)

	response, err := tr.LLM.Complete(ctx, []llm.Message{
		{Role: "user", Content: prompt},
	}, &llm.CompletionOpts{
		Model:       "mistral-large-latest",
		Temperature: 0.1,
		MaxTokens:   500,
	})
	if err != nil {
		log.Printf("[TIER] LLM analyze error: %v, fallback to Tier2", err)
		return tr.fallbackTier(query), nil
	}

	return parseTierResult(response.Content, query), nil
}

func (tr *TierRouter) fallbackTier(query string) *TierResult {
	lower := strings.ToLower(query)
	simpleWords := []string{"простой", "simple", "easy", "basic", "hello", "hi", "привет"}
	heavyWords := []string{"project", "compile", "test", "archive", "база данных", "deploy", "много файлов"}

	isSimple := false
	for _, w := range simpleWords {
		if strings.Contains(lower, w) {
			isSimple = true
			break
		}
	}

	heavyCount := 0
	for _, w := range heavyWords {
		if strings.Contains(lower, w) {
			heavyCount++
		}
	}

	if isSimple || len(query) < 50 {
		return &TierResult{Tier: Tier1Direct, Confidence: 80, MaxSteps: 1, Timeout: 60 * time.Second, IsSimple: true}
	}
	if heavyCount >= 2 || len(query) > 200 {
		return &TierResult{Tier: Tier3MultiAgent, Confidence: 70, MaxSteps: 15, Timeout: 300 * time.Second, IsSimple: false}
	}
	return &TierResult{Tier: Tier2SingleAgent, Confidence: 70, MaxSteps: 4, Timeout: 120 * time.Second, IsSimple: false}
}

func parseTierResult(text, query string) *TierResult {
	result := &TierResult{
		Tier:       Tier2SingleAgent,
		Confidence: 50,
		MaxSteps:   4,
		Timeout:    120 * time.Second,
		IsSimple:   false,
		NeedsTools: []string{},
	}

	lower := strings.ToLower(query)
	if strings.Contains(lower, "простой") || strings.Contains(lower, "simple") ||
		strings.Contains(lower, "easy") || strings.Contains(lower, "basic") {
		result.IsSimple = true
	}

	lines := strings.Split(text, "\n")
	for _, line := range lines {
		line = strings.TrimSpace(line)
		if strings.HasPrefix(line, "TIER:") {
			var tier int
			fmt.Sscanf(line, "TIER: %d", &tier)
			switch tier {
			case 1:
				result.Tier = Tier1Direct
			case 2:
				result.Tier = Tier2SingleAgent
			case 3:
				result.Tier = Tier3MultiAgent
			}
		}
		if strings.HasPrefix(line, "CONFIDENCE:") {
			fmt.Sscanf(line, "CONFIDENCE: %f", &result.Confidence)
		}
		if strings.HasPrefix(line, "REASONING:") {
			result.Reasoning = strings.TrimSpace(strings.TrimPrefix(line, "REASONING:"))
		}
		if strings.HasPrefix(line, "MAX_STEPS:") {
			fmt.Sscanf(line, "MAX_STEPS: %d", &result.MaxSteps)
			if result.MaxSteps < 1 {
				result.MaxSteps = 1
			}
			if result.MaxSteps > 15 {
				result.MaxSteps = 15
			}
		}
		if strings.HasPrefix(line, "TIMEOUT_SEC:") {
			var sec int
			fmt.Sscanf(line, "TIMEOUT_SEC: %d", &sec)
			if sec >= 30 && sec <= 300 {
				result.Timeout = time.Duration(sec) * time.Second
			}
		}
		if strings.HasPrefix(line, "IS_SIMPLE:") {
			val := strings.TrimSpace(strings.TrimPrefix(line, "IS_SIMPLE:"))
			result.IsSimple = val == "true" || val == "True" || val == "TRUE"
		}
		if strings.HasPrefix(line, "NEEDS_TOOLS:") {
			toolsStr := strings.TrimSpace(strings.TrimPrefix(line, "NEEDS_TOOLS:"))
			if toolsStr != "" && toolsStr != "none" {
				result.NeedsTools = strings.Split(toolsStr, ",")
			}
		}
	}

	if result.IsSimple {
		result.Tier = Tier1Direct
		result.MaxSteps = 1
		if result.Timeout > 60*time.Second {
			result.Timeout = 60 * time.Second
		}
	}

	return result
}

// TierExecutor — выполняет запрос согласно tier
type TierExecutor struct {
	Agent        *Agent
	Router       *TierRouter
	Orchestrator *Orchestrator
}

func NewTierExecutor(agent *Agent, router *TierRouter, orch *Orchestrator) *TierExecutor {
	return &TierExecutor{
		Agent:        agent,
		Router:       router,
		Orchestrator: orch,
	}
}

// Run — главный entry point, заменяет Agent.Run()
func (te *TierExecutor) Run(ctx context.Context, chatID int64, task string) (*AgentResult, error) {
	log.Printf("[TIER] ════════════════════════════════════════")
	log.Printf("[TIER] 🚀 Analyzing task: chat=%d", chatID)
	log.Printf("[TIER] 📝 Task: %q", task)

	tier, err := te.Router.Analyze(ctx, task)
	if err != nil {
		log.Printf("[TIER] ⚠️ Analyze error: %v, using fallback", err)
		tier = te.Router.fallbackTier(task)
	}

	log.Printf("[TIER] 📊 DECISION: tier=%d, confidence=%.0f%%, maxSteps=%d, timeout=%v, simple=%v",
		tier.Tier, tier.Confidence, tier.MaxSteps, tier.Timeout, tier.IsSimple)
	if tier.Reasoning != "" {
		log.Printf("[TIER] 💭 Reasoning: %s", tier.Reasoning)
	}

	ctx, cancel := context.WithTimeout(ctx, tier.Timeout)
	defer cancel()

	switch tier.Tier {
	case Tier1Direct:
		return te.runTier1(ctx, chatID, task, tier)
	case Tier2SingleAgent:
		return te.runTier2(ctx, chatID, task, tier)
	case Tier3MultiAgent:
		return te.runTier3(ctx, chatID, task, tier)
	default:
		return te.runTier2(ctx, chatID, task, tier)
	}
}

// Tier 1: Прямой ответ, 1 LLM вызов, никаких тулов
func (te *TierExecutor) runTier1(ctx context.Context, chatID int64, task string, tier *TierResult) (*AgentResult, error) {
	log.Printf("[TIER1] ⚡ Direct response mode")

	messages := []llm.Message{
		{Role: "system", Content: buildTier1SystemPrompt(tier.IsSimple)},
		{Role: "user", Content: task},
	}

	response, err := te.Agent.LLM.Complete(ctx, messages, &llm.CompletionOpts{
		Model:       "mistral-large-latest",
		Temperature: 0.7,
		MaxTokens:   4000,
	})
	if err != nil {
		return nil, err
	}

	result := &AgentResult{
		Answer:        response.Content,
		InputTokens:   response.InputTokens,
		OutputTokens:  response.OutputTokens,
		TotalTokens:   response.TotalTokens,
		AgentType:     "tier1_direct",
		ThinkingNotes: []string{"Direct response — no tools needed"},
	}

	if HasToolCalls(result.Answer) {
		log.Printf("[TIER1] ⚠️ Unexpected tool calls in Tier1, falling back to Tier2")
		return te.runTier2(ctx, chatID, task, tier)
	}

	te.Agent.Memory.SaveMessage(chatID, "user", task)
	te.Agent.Memory.SaveMessage(chatID, "assistant", result.Answer)

	log.Printf("[TIER1] ✅ Done: %d tokens, %d chars", result.TotalTokens, len(result.Answer))
	return result, nil
}

// Tier 2: Один агент, ограниченные шаги
func (te *TierExecutor) runTier2(ctx context.Context, chatID int64, task string, tier *TierResult) (*AgentResult, error) {
	log.Printf("[TIER2] 🔧 Single agent mode, maxSteps=%d", tier.MaxSteps)

	result := &AgentResult{AgentType: "tier2_single"}
	te.Agent.Memory.SaveMessage(chatID, "user", task)

	skillPrompt := ""
	if te.Agent.Skills != nil {
		skillPrompt = te.Agent.Skills.BuildPromptRelevant(task, 3)
	}

	for step := 0; step < tier.MaxSteps; step++ {
		log.Printf("[TIER2] ───── Step %d/%d ─────", step+1, tier.MaxSteps)

		history, _ := te.Agent.Memory.LoadHistory(chatID, 30)
		messages := te.buildTier2Messages(history, task, skillPrompt, tier)

		response, err := te.Agent.LLM.Complete(ctx, messages, &llm.CompletionOpts{
			Model:       "mistral-large-latest",
			Temperature: 0.7,
			MaxTokens:   8000,
		})
		if err != nil {
			log.Printf("[TIER2] ❌ LLM error: %v", err)
			return nil, err
		}

		result.InputTokens += response.InputTokens
		result.OutputTokens += response.OutputTokens
		result.TotalTokens += response.TotalTokens

		calls := ParseToolCalls(response.Content)
		log.Printf("[TIER2] Tool calls: %d", len(calls))

		if len(calls) == 0 {
			result.Answer = response.Content
			log.Printf("[TIER2] ✅ Final answer: %d chars", len(result.Answer))
			break
		}

		te.Agent.Memory.SaveMessage(chatID, "assistant", response.Content)

		for _, call := range calls {
			result.AgentLog = append(result.AgentLog, fmt.Sprintf("Step %d: %s", step+1, call.Tool))

			var output string
			var err error

			if te.Agent.MCP != nil {
				output, err = te.Agent.MCP.CallToolAuto(ctx, call.Tool, call.Args)
				if err == nil {
					te.Agent.Memory.SaveMessage(chatID, "user", fmt.Sprintf("Tool <%s>: %s", call.Tool, output))
					if call.Tool == "thinking.note" {
						if note, ok := call.Args["note"].(string); ok {
							result.ThinkingNotes = append(result.ThinkingNotes, note)
						}
					}
					continue
				}
			}

			output, err = te.Agent.Executor.Execute(ctx, chatID, 0, call)
			if err != nil {
				output = fmt.Sprintf("Error: %v", err)
			}

			te.Agent.Memory.SaveMessage(chatID, "user", fmt.Sprintf("Tool <%s>: %s", call.Tool, output))

			if call.Tool == "thinking.note" {
				if note, ok := call.Args["note"].(string); ok {
					result.ThinkingNotes = append(result.ThinkingNotes, note)
				}
			}
		}
	}

	if result.Answer != "" {
		te.Agent.Memory.SaveMessage(chatID, "assistant", result.Answer)
	}

	log.Printf("[TIER2] ✅ Done: %d tokens", result.TotalTokens)
	return result, nil
}

// Tier 3: Мультиагент через оркестратор
func (te *TierExecutor) runTier3(ctx context.Context, chatID int64, task string, tier *TierResult) (*AgentResult, error) {
	log.Printf("[TIER3] 🔀 Multi-agent mode")

	if te.Orchestrator != nil {
		analysis, err := te.Orchestrator.AnalyzeTask(ctx, task)
		if err == nil && analysis.Confidence > 50 {
			subAgent := te.Orchestrator.CreateSubAgent(analysis, te.Agent.Skills)
			subAgent.MaxSteps = tier.MaxSteps
			result, err := subAgent.Run(ctx, chatID, task)
			if err == nil {
				result.AgentType = string(analysis.AgentType)
				log.Printf("[TIER3] ✅ SubAgent success")
				return result, nil
			}
			log.Printf("[TIER3] ⚠️ SubAgent failed: %v, fallback to Tier2", err)
		}
	}

	tier.MaxSteps = 15
	return te.runTier2(ctx, chatID, task, tier)
}

func buildTier1SystemPrompt(isSimple bool) string {
	var sb strings.Builder
	sb.WriteString("You are XLI-Go Bot, a helpful AI assistant.\n")
	sb.WriteString("CRITICAL RULES:\n")
	sb.WriteString("1. Be CONCISE. 2-3 sentences max unless asked for details.\n")
	sb.WriteString("2. If user asks for code, write it inline in markdown blocks.\n")
	sb.WriteString("3. Do NOT use any tools. Just answer directly.\n")
	sb.WriteString("4. If user says 'simple' or 'простой', give MINIMAL solution (1 file, <50 lines).\n")
	sb.WriteString("5. NO venv, NO pip install, NO complex setup for simple requests.\n")
	return sb.String()
}

func (te *TierExecutor) buildTier2Messages(history []memory.Message, task, skillPrompt string, tier *TierResult) []llm.Message {
	var messages []llm.Message

	var sb strings.Builder
	sb.WriteString("You are XLI-Go Bot, an AI assistant with tools.\n")
	sb.WriteString("CRITICAL RULES:\n")
	sb.WriteString("1. Be CONCISE. Use tools efficiently.\n")
	sb.WriteString("2. Use file.write for code, NEVER write code in text response.\n")
	sb.WriteString("3. Use thinking.note to plan briefly (1 sentence).\n")
	sb.WriteString("4. If user said 'simple' or 'простой': 1 file, <50 lines, NO dependencies.\n")
	sb.WriteString("5. Use ```tool_call format for tools.\n")
	sb.WriteString("6. After writing .go, you may compile with terminal.run.\n")
	sb.WriteString("7. Use archive.create to pack multiple files.\n")
	sb.WriteString("8. STOP when task is done. Do not over-engineer.\n")

	sb.WriteString("\nBuilt-in tools:")
	sb.WriteString("```tool_call\n")
	sb.WriteString(`{"tool":"thinking.note","args":{"note":"brief plan"}}`)
	sb.WriteString("\n```")
	sb.WriteString("```tool_call\n")
	sb.WriteString(`{"tool":"terminal.run","args":{"cmd":"command"}}`)
	sb.WriteString("\n```")
	sb.WriteString("```tool_call\n")
	sb.WriteString(`{"tool":"file.read","args":{"path":"/path"}}`)
	sb.WriteString("\n```")
	sb.WriteString("```tool_call\n")
	sb.WriteString(`{"tool":"file.write","args":{"path":"/path","content":"data"}}`)
	sb.WriteString("\n```")
	sb.WriteString("```tool_call\n")
	sb.WriteString(`{"tool":"web.search","args":{"query":"search"}}`)
	sb.WriteString("\n```")
	sb.WriteString("```tool_call\n")
	sb.WriteString(`{"tool":"web.fetch","args":{"url":"https://..."}}`)
	sb.WriteString("\n```")
	sb.WriteString("```tool_call\n")
	sb.WriteString(`{"tool":"archive.create","args":{"source":"/path","name":"archive.zip"}}`)
	sb.WriteString("\n```")
	sb.WriteString("```tool_call\n")
	sb.WriteString(`{"tool":"sandbox.run","args":{"path":"/path/to/script"}}`)
	sb.WriteString("\n```")

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
