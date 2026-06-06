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
	"github.com/oblachko/xli-bot/internal/skills"
	"github.com/oblachko/xli-bot/internal/transport"
)

func main() {
	cfg, err := config.Load()
	if err != nil {
		log.Fatal(err)
	}

	os.MkdirAll("data", 0755)
	os.MkdirAll("skills", 0755)
	os.MkdirAll("mcp_servers", 0755)

	// SQLite
	store, err := memory.NewSQLiteStore("data/xli.db")
	if err != nil {
		log.Fatal("SQLite failed:", err)
	}
	defer store.Close()

	// Skills (hot reload)
	skillRegistry := skills.NewHotLoader()
	if err := skillRegistry.LoadFromDir("skills"); err != nil {
		log.Printf("Skills warning: %v", err)
	}

	// MCP (lazy)
	mcpClient := mcp.NewClient()
	mcpServers, err := mcp.AutoDiscover("mcp_servers")
	if err != nil {
		log.Printf("MCP warning: %v", err)
	}
	for _, server := range mcpServers {
		mcpClient.Register(server)
	}

	// LLM
	llmClient := llm.NewMistralClient(cfg.LLM.APIKey, cfg.LLM.Provider)

	// Telegram
	tg, err := transport.NewTelegram(cfg.Telegram.BotToken, nil)
	if err != nil {
		log.Fatal(err)
	}

	// Agent
	executor := agent.NewToolExecutor(tg)
	botAgent := agent.NewAgent(llmClient, store, executor, skillRegistry, mcpClient)
	tg.SetAgent(botAgent)

	// Background: cleanup + reconnect
	go func() {
		ticker := time.NewTicker(5 * time.Minute)
		defer ticker.Stop()
		for range ticker.C {
			mcpClient.CleanupIdle()
			mcpClient.AutoReconnect()
		}
	}()

	log.Println("🤖 XLI Bot v2 started!")
	log.Printf("📦 MCP servers: %d", len(mcpServers))
	log.Printf("📚 Skills: hot-reload enabled")

	tg.Start()
}
