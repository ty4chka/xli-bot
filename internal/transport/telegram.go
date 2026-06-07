package transport

import (
	"context"
	"fmt"
	"log"
	"strings"
	"sync"
	"time"

	tgbotapi "github.com/go-telegram-bot-api/telegram-bot-api/v5"

	"github.com/oblachko/xli-bot/internal/agent"
	"github.com/oblachko/xli-bot/internal/utils"
)

type TelegramTransport struct {
	bot           *tgbotapi.BotAPI
	agent         *agent.Agent
	confirmations map[string]chan bool
	mu            sync.RWMutex
}

func NewTelegram(token string, a *agent.Agent) (*TelegramTransport, error) {
	bot, err := tgbotapi.NewBotAPI(token)
	if err != nil {
		return nil, err
	}

	return &TelegramTransport{
		bot:           bot,
		agent:         a,
		confirmations: make(map[string]chan bool),
	}, nil
}

func (t *TelegramTransport) SetAgent(a *agent.Agent) {
	t.agent = a
}

func (t *TelegramTransport) Start() {
	u := tgbotapi.NewUpdate(0)
	u.Timeout = 60

	updates := t.bot.GetUpdatesChan(u)
	log.Printf("Bot @%s started", t.bot.Self.UserName)

	for update := range updates {
		if update.Message != nil {
			go t.handleMessage(update.Message)
		}
		if update.CallbackQuery != nil {
			go t.handleCallback(update.CallbackQuery)
		}
	}
}

func (t *TelegramTransport) handleMessage(msg *tgbotapi.Message) {
	chatID := msg.Chat.ID
	log.Printf("[MSG] chat=%d text=%q", chatID, msg.Text)

	if msg.IsCommand() {
		t.handleCommand(msg)
		return
	}

	t.processAgentRequest(chatID, msg.Text)
}

func (t *TelegramTransport) handleCommand(msg *tgbotapi.Message) {
	chatID := msg.Chat.ID
	log.Printf("[CMD] chat=%d cmd=%s", chatID, msg.Command())

	switch msg.Command() {
	case "start":
		t.sendMessage(chatID, "XLI Bot started!\n\nUse `/oa <query>` or just text me.")

	case "help":
		help := "Commands:\n" +
			"`/oa <query>` - ask agent\n" +
			"`/clear` - clear memory\n" +
			"`/skills` - list skills\n" +
			"`/mcp` - MCP status\n" +
			"`/status` - bot status\n\n" +
			"Just text me - I will respond."
		t.sendMessage(chatID, help)

	case "clear":
		t.agent.Memory.ClearHistory(chatID)
		t.sendMessage(chatID, "Memory cleared!")

	case "status":
		t.sendMessage(chatID, "Bot running\nSQLite connected")

	case "skills":
		t.handleSkillsCommand(chatID)

	case "mcp":
		t.handleMCPCommand(chatID)

	case "oa":
		query := msg.CommandArguments()
		if query == "" {
			t.sendMessage(chatID, "Usage: `/oa write Go code`")
			return
		}
		t.processAgentRequest(chatID, query)

	default:
		t.sendMessage(chatID, "Unknown command. Use `/help`")
	}
}

func (t *TelegramTransport) handleSkillsCommand(chatID int64) {
	all := t.agent.Skills.GetAll()
	active := t.agent.Skills.GetActive()
	activeMap := make(map[string]bool)
	for _, a := range active {
		activeMap[a.Name] = true
	}

	var sb strings.Builder
	sb.WriteString("Skills:\n\n")
	for _, s := range all {
		status := "o"
		if activeMap[s.Name] {
			status = "+"
		}
		if s.TriggerMode == "always" {
			status = "*"
		}
		sb.WriteString(fmt.Sprintf("%s %s (%s)\n", status, s.Name, s.TriggerMode))
	}
	t.sendMessage(chatID, sb.String())
}

func (t *TelegramTransport) handleMCPCommand(chatID int64) {
	tools := t.agent.MCP.ListAllTools()
	var sb strings.Builder
	sb.WriteString("MCP tools:\n\n")
	if len(tools) == 0 {
		sb.WriteString("No servers connected")
	} else {
		for _, tool := range tools {
			sb.WriteString(fmt.Sprintf("- %s: %s\n", tool.Name, tool.Description))
		}
	}
	t.sendMessage(chatID, sb.String())
}

func (t *TelegramTransport) processAgentRequest(chatID int64, text string) {
	log.Printf("[AGENT] chat=%d query=%q", chatID, text)
	ctx, cancel := context.WithTimeout(context.Background(), 120*time.Second)
	defer cancel()

	thinkMsg, err := t.sendMessage(chatID, "Thinking...")
	if err != nil {
		log.Printf("[ERROR] send thinking: %v", err)
		return
	}
	log.Printf("[AGENT] thinking msg=%d", thinkMsg.MessageID)

	result, err := t.agent.Run(ctx, chatID, text)
	if err != nil {
		log.Printf("[ERROR] agent run: %v", err)
		t.editMessage(chatID, thinkMsg.MessageID, "Error: "+err.Error())
		return
	}

	log.Printf("[AGENT] result: tokens=%d answer_len=%d", result.TotalTokens, len(result.Answer))
	response := utils.FormatResponse(result.Answer)
	tokenInfo := utils.FormatTokenUsage(result.InputTokens, result.OutputTokens, result.TotalTokens)
	finalText := response + "\n\n" + tokenInfo

	keyboard := tgbotapi.NewInlineKeyboardMarkup(
		tgbotapi.NewInlineKeyboardRow(
			tgbotapi.NewInlineKeyboardButtonData("Clear", fmt.Sprintf("action:clear:%d", chatID)),
			tgbotapi.NewInlineKeyboardButtonData("Regen", fmt.Sprintf("action:regen:%d:%s", chatID, text)),
		),
	)

	t.editMessageWithKeyboard(chatID, thinkMsg.MessageID, finalText, keyboard)
	log.Printf("[AGENT] done")
}

func (t *TelegramTransport) handleCallback(query *tgbotapi.CallbackQuery) {
	chatID := query.Message.Chat.ID
	msgID := query.Message.MessageID
	data := query.Data
	log.Printf("[CB] chat=%d data=%q", chatID, data)

	parts := strings.Split(data, ":")
	if len(parts) < 2 {
		t.bot.Request(tgbotapi.NewCallback(query.ID, ""))
		return
	}

	switch parts[0] {
	case "confirm":
		if len(parts) >= 3 {
			token := parts[2]
			approved := parts[1] == "yes"
			t.mu.RLock()
			ch, ok := t.confirmations[token]
			t.mu.RUnlock()
			if ok {
				ch <- approved
			}
			t.editMessage(chatID, msgID, query.Message.Text)
		}

	case "action":
		switch parts[1] {
		case "clear":
			t.agent.Memory.ClearHistory(chatID)
			t.editMessage(chatID, msgID, "Memory cleared!")
		case "regen":
			if len(parts) >= 3 {
				originalText := strings.Join(parts[2:], ":")
				t.bot.Request(tgbotapi.NewDeleteMessage(chatID, msgID))
				t.processAgentRequest(chatID, originalText)
			}
		}
	}

	t.bot.Request(tgbotapi.NewCallback(query.ID, ""))
}

func (t *TelegramTransport) ShowConfirmation(chatID int64, msgID int, toolName string, args map[string]interface{}) (bool, error) {
	token := fmt.Sprintf("confirm_%d_%d", chatID, time.Now().UnixNano())

	t.mu.Lock()
	t.confirmations[token] = make(chan bool, 1)
	t.mu.Unlock()

	argsStr := formatArgs(args)
	text := fmt.Sprintf("Confirm action:\n\nTool: %s\nArgs:\n%s\n\nExecute?", toolName, argsStr)

	keyboard := tgbotapi.NewInlineKeyboardMarkup(
		tgbotapi.NewInlineKeyboardRow(
			tgbotapi.NewInlineKeyboardButtonData("Yes", fmt.Sprintf("confirm:yes:%s", token)),
			tgbotapi.NewInlineKeyboardButtonData("No", fmt.Sprintf("confirm:no:%s", token)),
		),
	)

	t.editMessageWithKeyboard(chatID, msgID, text, keyboard)

	select {
	case approved := <-t.confirmations[token]:
		t.mu.Lock()
		delete(t.confirmations, token)
		t.mu.Unlock()
		return approved, nil
	case <-time.After(60 * time.Second):
		t.mu.Lock()
		delete(t.confirmations, token)
		t.mu.Unlock()
		return false, fmt.Errorf("timeout")
	}
}

func formatArgs(args map[string]interface{}) string {
	var parts []string
	for k, v := range args {
		parts = append(parts, fmt.Sprintf("  - %s: %v", k, v))
	}
	return strings.Join(parts, "\n")
}

func (t *TelegramTransport) SendFileBytes(chatID int64, name string, data []byte, caption string) error {
	file := tgbotapi.NewDocument(chatID, tgbotapi.FileBytes{Name: name, Bytes: data})
	if caption != "" {
		file.Caption = caption
		file.ParseMode = tgbotapi.ModeMarkdownV2
	}
	_, err := t.bot.Send(file)
	return err
}

func (t *TelegramTransport) sendMessage(chatID int64, text string) (tgbotapi.Message, error) {
	msg := tgbotapi.NewMessage(chatID, text)
	msg.ParseMode = tgbotapi.ModeMarkdownV2
	return t.bot.Send(msg)
}

func (t *TelegramTransport) editMessage(chatID int64, msgID int, text string) {
	edit := tgbotapi.NewEditMessageText(chatID, msgID, text)
	edit.ParseMode = tgbotapi.ModeMarkdownV2
	t.bot.Request(edit)
}

func (t *TelegramTransport) editMessageWithKeyboard(chatID int64, msgID int, text string, keyboard tgbotapi.InlineKeyboardMarkup) {
	edit := tgbotapi.NewEditMessageText(chatID, msgID, text)
	edit.ParseMode = tgbotapi.ModeMarkdownV2
	edit.ReplyMarkup = &keyboard
	t.bot.Request(edit)
}
