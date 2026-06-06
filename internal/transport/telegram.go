package transport

import (
	"context"
	"fmt"
	"log"
	"strconv"
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

	if msg.IsCommand() {
		t.handleCommand(msg)
		return
	}

	t.processAgentRequest(chatID, msg.Text)
}

func (t *TelegramTransport) handleCommand(msg *tgbotapi.Message) {
	chatID := msg.Chat.ID

	switch msg.Command() {
	case "start":
		t.sendMessage(chatID, "🤖 *XLI Bot* запущен!\n\nИспользуй `/oa <запрос>` или просто пиши.")

	case "help":
		help := "📋 *Команды:*\n" +
			"`/oa <запрос>` — спросить агента\n" +
			"`/clear` — очистить память\n" +
			"`/skills` — список скиллов\n" +
			"`/mcp` — статус MCP\n" +
			"`/status` — статус бота\n\n" +
			"Просто пиши текст — я отвечу."
		t.sendMessage(chatID, help)

	case "clear":
		t.agent.Memory.ClearHistory(chatID)
		t.sendMessage(chatID, "🧹 *Память очищена!*")

	case "status":
		t.sendMessage(chatID, "✅ *Бот работает*\n💾 SQLite подключена")

	case "skills":
		t.handleSkillsCommand(chatID)

	case "mcp":
		t.handleMCPCommand(chatID)

	case "oa":
		query := msg.CommandArguments()
		if query == "" {
			t.sendMessage(chatID, "❌ Укажи запрос: `/oa напиши код на Go`")
			return
		}
		t.processAgentRequest(chatID, query)

	default:
		t.sendMessage(chatID, "❓ Неизвестная команда. `/help`")
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
	sb.WriteString("📚 *Скиллы:*\n\n")
	for _, s := range all {
		status := "⚪"
		if activeMap[s.Name] {
			status = "🟢"
		}
		if s.TriggerMode == "always" {
			status = "🔒"
		}
		sb.WriteString(fmt.Sprintf("%s `%s` (%s)\n", status, s.Name, s.TriggerMode))
	}
	t.sendMessage(chatID, sb.String())
}

func (t *TelegramTransport) handleMCPCommand(chatID int64) {
	tools := t.agent.MCP.ListAllTools()
	var sb strings.Builder
	sb.WriteString("📦 *MCP тулы:*\n\n")
	if len(tools) == 0 {
		sb.WriteString("_Нет подключенных серверов_")
	} else {
		for _, tool := range tools {
			sb.WriteString(fmt.Sprintf("• `%s` — %s\n", tool.Name, tool.Description))
		}
	}
	t.sendMessage(chatID, sb.String())
}

func (t *TelegramTransport) processAgentRequest(chatID int64, text string) {
	ctx, cancel := context.WithTimeout(context.Background(), 120*time.Second)
	defer cancel()

	// "Думаю..."
	thinkMsg, _ := t.sendMessage(chatID, "⏳ *Думаю...*")

	result, err := t.agent.Run(ctx, chatID, text)
	if err != nil {
		t.editMessage(chatID, thinkMsg.MessageID, "❌ *Ошибка:* "+utils.EscapeMarkdownV2(err.Error()))
		return
	}

	response := utils.FormatResponse(result.Answer)
	tokenInfo := utils.FormatTokenUsage(result.InputTokens, result.OutputTokens, result.TotalTokens)
	finalText := response + "\n\n" + tokenInfo

	// Inline кнопки
	keyboard := tgbotapi.NewInlineKeyboardMarkup(
		tgbotapi.NewInlineKeyboardRow(
			tgbotapi.NewInlineKeyboardButtonData("🧹 Очистить", fmt.Sprintf("action:clear:%d", chatID)),
			tgbotapi.NewInlineKeyboardButtonData("🔃 Ещё раз", fmt.Sprintf("action:regen:%d:%s", chatID, utils.EscapeMarkdownV2(text))),
		),
	)

	t.editMessageWithKeyboard(chatID, thinkMsg.MessageID, finalText, keyboard)
}

func (t *TelegramTransport) handleCallback(query *tgbotapi.CallbackQuery) {
	chatID := query.Message.Chat.ID
	msgID := query.Message.MessageID
	data := query.Data

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
			t.editMessage(chatID, msgID, "🧹 *Память очищена!*")
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
	text := fmt.Sprintf("⚠️ *Подтверди действие:*\n\n*Тул:* `%s`\n*Аргументы:*\n%s\n\nВыполнить?", toolName, argsStr)

	keyboard := tgbotapi.NewInlineKeyboardMarkup(
		tgbotapi.NewInlineKeyboardRow(
			tgbotapi.NewInlineKeyboardButtonData("✅ Выполнить", fmt.Sprintf("confirm:yes:%s", token)),
			tgbotapi.NewInlineKeyboardButtonData("❌ Отмена", fmt.Sprintf("confirm:no:%s", token)),
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
		parts = append(parts, fmt.Sprintf("  • `%s`: `%v`", k, v))
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
