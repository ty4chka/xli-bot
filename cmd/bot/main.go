package main

import (
	"log"
	"os"
	"time"

	"github.com/oblachko/xli-bot/internal/agent"
	"github.com/oblachko/xli-bot/internal/config"
	"github.com/oblachko/xli-bot/internal/llm"
	"github.com/oblachko/xli-bot/internal/mcp"
	"github.com/oblachko/xli-bot/internal/memory"
	"github.com/oblachko/xli-bot/internal/sandbox"
	"github.com/oblachko/xli-bot/internal/skills"
	"github.com/oblachko/xli-bot/internal/transport"
)

func main() {
	cfg, err := config.Load()
	if err != nil {
		log.Fatal(err)
	}

	log.Printf("Config loaded: Telegram token len=%d, LLM provider=%s", len(cfg.Telegram.BotToken), cfg.LLM.Provider)
	if cfg.Telegram.BotToken == "" {
		log.Fatal("TELEGRAM_BOT_TOKEN is empty!")
	}
	if cfg.LLM.APIKey == "" {
		log.Fatal("LLM_API_KEY is empty!")
	}

	os.MkdirAll("data", 0755)
	os.MkdirAll("skills", 0755)
	os.MkdirAll("mcp_servers", 0755)
	os.MkdirAll("sandbox", 0755)

	store, err := memory.NewSQLiteStore("data/xli.db")
	if err != nil {
		log.Fatal("SQLite failed:", err)
	}
	defer store.Close()
	log.Println("SQLite OK")

	skillRegistry := skills.NewHotLoader()
	if err := skillRegistry.LoadFromDir("skills"); err != nil {
		log.Printf("Skills warning: %v", err)
	}
	log.Printf("Skills loaded: %d", len(skillRegistry.GetAll()))

	// MCP — регистрация серверов
	mcpClient := mcp.NewClient()

	// Ручная регистрация всех MCP серверов
	mcpServers := []mcp.MCPServer{
		{Name: "debugger", Script: "./mcp_servers/mcp-debugger-cli.py", Enabled: true},
		{Name: "knowledge", Script: "./mcp_servers/mcp-knowledge-cli.py", Enabled: true},
		{Name: "package-monitor", Script: "./mcp_servers/mcp-package-monitor-cli.py", Enabled: true},
		{Name: "shell-helper", Script: "./mcp_servers/mcp-shell-helper-cli.py", Enabled: true},
		{Name: "prompt-cli", Script: "./mcp_servers/prompt-cli.py", Enabled: true},
		{Name: "archaeologist", Script: "./mcp_servers/archaeologist_cli.py", Enabled: true},
		{Name: "refactor", Script: "./mcp_servers/refactor_cli.py", Enabled: true},
		{Name: "architecture", Script: "./mcp_servers/architecture_cli.py", Enabled: true},
		{Name: "auto-tester", Script: "./mcp_servers/mcp-auto-tester-cli.py", Enabled: true},
	}

	for _, srv := range mcpServers {
		if err := mcpClient.Register(srv); err != nil {
			log.Printf("MCP register error %s: %v", srv.Name, err)
		} else {
			log.Printf("MCP registered: %s", srv.Name)
		}
	}

	// AutoDiscover как fallback
	autoServers, err := mcp.AutoDiscover("mcp_servers")
	if err != nil {
		log.Printf("MCP autodiscover warning: %v", err)
	} else {
		for _, srv := range autoServers {
			if err := mcpClient.Register(srv); err != nil {
				log.Printf("MCP autodiscover register error %s: %v", srv.Name, err)
			}
		}
	}

	log.Printf("MCP servers: %d registered (lazy load)", len(mcpClient.GetServerNames()))

	llmClient := llm.NewMistralClient(cfg.LLM.APIKey, cfg.LLM.Provider)
	log.Println("LLM client created")

	tg, err := transport.NewTelegram(cfg.Telegram.BotToken, nil)
	if err != nil {
		log.Fatal(err)
	}
	log.Println("Telegram transport created")

	sbx, _ := sandbox.NewSandbox("sandbox")
	executor := agent.NewToolExecutor(tg, sbx)
	botAgent := agent.NewAgent(llmClient, store, executor, skillRegistry, mcpClient)
	orchestrator := agent.NewOrchestrator(botAgent, llmClient)
	botAgent.SetOrchestrator(orchestrator)
	tg.SetAgent(botAgent)
	log.Println("Agent + Orchestrator created")

	go func() {
		ticker := time.NewTicker(5 * time.Minute)
		defer ticker.Stop()
		for range ticker.C {
			mcpClient.CleanupIdle()
		}
	}()

	log.Println("XLI Bot v2 started!")
	tg.Start()
}
