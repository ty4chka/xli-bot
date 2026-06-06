
package transport

import (
	"context"
	"fmt"
	"log"
	"strings"
	"time"

	"github.com/go-telegram-bot-api/telegram-bot-api/v5"
	"github.com/oblachko/xli-bot/internal/agent"
	"github.com/oblachko/xli-bot/internal/config"
)

// TelegramTransport handles Telegram bot interactions
type TelegramTransport struct {
	bot    *tgbotapi.BotAPI
	agent  *agent.Agent
	config *config.Config
}

// NewTelegramTransport creates a new Telegram transport
func NewTelegramTransport(cfg *config.Config, ag *agent.Agent) (*TelegramTransport, error) {
	bot, err := tgbotapi.NewBotAPI(cfg.TelegramToken)
	if err != nil {
		return nil, fmt.Errorf("create bot: %w", err)
	}

	bot.Debug = false
	log.Printf("Authorized on account %s", bot.Self.UserName)

	return &TelegramTransport{
		bot:    bot,
		agent:  ag,
		config: cfg,
	}, nil
}

// Start begins polling for updates
func (t *TelegramTransport) Start() error {
	u := tgbotapi.NewUpdate(0)
	u.Timeout = 60

	updates := t.bot.GetUpdatesChan(u)

	for update := range updates {
		if update.Message != nil {
			go t.handleMessage(update.Message)
		} else if update.CallbackQuery != nil {
			go t.handleCallback(update.CallbackQuery)
		}
	}

	return nil
}

// handleMessage processes incoming messages
func (t *TelegramTransport) handleMessage(msg *tgbotapi.Message) {
	chatID := msg.Chat.ID
	text := msg.Text

	// Ignore empty messages
	if text == "" {
		return
	}

	// Handle commands
	if strings.HasPrefix(text, "/") {
		t.handleCommand(msg)
		return
	}

	// Regular message — process through agent
	t.processAgentRequest(chatID, text, msg)
}

// handleCommand processes bot commands
func (t *TelegramTransport) handleCommand(msg *tgbotapi.Message) {
	chatID := msg.Chat.ID
	parts := strings.Fields(msg.Text)
	if len(parts) == 0 {
		return
	}

	cmd := parts[0]

	switch cmd {
	case "/start":
		t.sendMessage(chatID, "XLI Bot ready!")

	case "/help":
		help := "Commands:\n/oa <query> - ask agent\n/clear - clear context\n/help - this help"
		t.sendMessage(chatID, help)

	case "/clear":
		t.sendMessage(chatID, "Context cleared")

	case "/oa":
		if len(parts) < 2 {
			t.sendMessage(chatID, "Usage: /oa <query>")
			return
		}
		query := strings.Join(parts[1:], " ")
		t.processAgentRequest(chatID, query, msg)

	default:
		t.sendMessage(chatID, "Unknown command. Use /help")
	}
}

// processAgentRequest runs the agent and handles the response
func (t *TelegramTransport) processAgentRequest(chatID int64, text string, msg *tgbotapi.Message) {
	ctx := context.Background()

	// Send "thinking" message
	thinkingMsg, err := t.sendMessage(chatID, "Thinking...")
	if err != nil {
		log.Printf("Error sending thinking message: %v", err)
		return
	}

	// Run agent
	result, err := t.agent.Run(ctx, text)
	if err != nil {
		t.editMessage(chatID, thinkingMsg.MessageID, fmt.Sprintf("Error: %s", escapeHTML(err.Error())))
		return
	}

	// Build final response
	response := t.formatResult(result)

	// Edit thinking message with final result
	t.editMessage(chatID, thinkingMsg.MessageID, response)
}

// formatResult formats the agent result for Telegram
func (t *TelegramTransport) formatResult(result *agent.AgentResult) string {
	var sb strings.Builder

	// Answer
	sb.WriteString(result.Answer)

	// Token usage
	if result.TokenUsage.TotalTokens > 0 {
		sb.WriteString(fmt.Sprintf(
			"\n\nTokens: in %d, out %d | total %d",
			result.TokenUsage.InputTokens,
			result.TokenUsage.OutputTokens,
			result.TokenUsage.TotalTokens,
		))
	}

	// Agent log
	if len(result.AgentLog) > 0 {
		sb.WriteString("\n\nLog: ")
		sb.WriteString(strings.Join(result.AgentLog, ", "))
	}

	return sb.String()
}

// handleCallback processes inline keyboard callbacks
func (t *TelegramTransport) handleCallback(query *tgbotapi.CallbackQuery) {
	// Acknowledge the callback
	callback := tgbotapi.NewCallback(query.ID, "")
	t.bot.Request(callback)

	data := query.Data
	chatID := query.Message.Chat.ID

	// Parse callback data
	parts := strings.Split(data, ":")
	if len(parts) < 2 {
		return
	}

	action := parts[0]

	switch action {
	case "confirm":
		t.editMessage(chatID, query.Message.MessageID, "Confirmed")
	case "cancel":
		t.editMessage(chatID, query.Message.MessageID, "Cancelled")
	}
}

// sendMessage sends a message and returns it
func (t *TelegramTransport) sendMessage(chatID int64, text string) (*tgbotapi.Message, error) {
	msg := tgbotapi.NewMessage(chatID, text)
	msg.ParseMode = "HTML"
	return t.bot.Send(msg)
}

// editMessage edits an existing message
func (t *TelegramTransport) editMessage(chatID int64, messageID int, text string) {
	edit := tgbotapi.NewEditMessageText(chatID, messageID, text)
	edit.ParseMode = "HTML"
	_, err := t.bot.Send(edit)
	if err != nil {
		log.Printf("Error editing message: %v", err)
	}
}

// escapeHTML escapes HTML special characters
func escapeHTML(text string) string {
	text = strings.ReplaceAll(text, "&", "&amp;")
	text = strings.ReplaceAll(text, "<", "&lt;")
	text = strings.ReplaceAll(text, ">", "&gt;")
	return text
}

// ShowConfirmation shows inline keyboard for confirmation
func (t *TelegramTransport) ShowConfirmation(chatID int64, messageID int, toolName string) error {
	keyboard := tgbotapi.NewInlineKeyboardMarkup(
		tgbotapi.NewInlineKeyboardRow(
			tgbotapi.NewInlineKeyboardButtonData("Execute", fmt.Sprintf("confirm:yes:%s", toolName)),
			tgbotapi.NewInlineKeyboardButtonData("Cancel", fmt.Sprintf("confirm:no:%s", toolName)),
		),
	)

	edit := tgbotapi.NewEditMessageTextAndMarkup(
		chatID, messageID,
		fmt.Sprintf("Confirm: %s?", toolName),
		keyboard,
	)
	edit.ParseMode = "HTML"
	_, err := t.bot.Send(edit)
	return err
}
