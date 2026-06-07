package transport

import (
	"context"
	"fmt"
	"html"
	"log"
	"strconv"
	"strings"
	"sync"
	"time"

	tgbotapi "github.com/go-telegram-bot-api/telegram-bot-api/v5"

	"github.com/oblachko/xli-bot/internal/agent"
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
	log.Printf("[TG] Bot @%s started", t.bot.Self.UserName)

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
	log.Printf("[TG] Message: chat=%d text=%q", chatID, msg.Text)

	if msg.IsCommand() {
		t.handleCommand(msg)
		return
	}

	t.processAgentRequest(chatID, msg.Text)
}

func (t *TelegramTransport) handleCommand(msg *tgbotapi.Message) {
	chatID := msg.Chat.ID
	log.Printf("[TG] Command: chat=%d cmd=%s", chatID, msg.Command())

	switch msg.Command() {
	case "start":
		t.sendHTML(chatID, "<b>XLI Bot</b> started!\n\nUse <code>/oa &lt;query&gt;</code> or just text me.")

	case "help":
		help := "<b>Commands:</b>\n" +
			"<code>/oa &lt;query&gt;</code> - ask agent\n" +
			"<code>/clear</code> - clear memory\n" +
			"<code>/skills</code> - list skills\n" +
			"<code>/mcp</code> - MCP status\n" +
			"<code>/status</code> - bot status\n\n" +
			"Just text me - I will respond."
		t.sendHTML(chatID, help)

	case "clear":
		t.agent.Memory.ClearHistory(chatID)
		t.sendHTML(chatID, "<b>Memory cleared!</b>")

	case "status":
		t.sendHTML(chatID, "Bot running\nSQLite connected")

	case "skills":
		t.handleSkillsCommand(chatID, 0)

	case "mcp":
		t.handleMCPCommand(chatID, 0)

	case "oa":
		query := msg.CommandArguments()
		if query == "" {
			t.sendHTML(chatID, "Usage: <code>/oa write Go code</code>")
			return
		}
		t.processAgentRequest(chatID, query)

	default:
		t.sendHTML(chatID, "Unknown command. Use <code>/help</code>")
	}
}

func (t *TelegramTransport) handleSkillsCommand(chatID int64, page int) {
	all := t.agent.Skills.GetAll()
	active := t.agent.Skills.GetActive()
	activeMap := make(map[string]bool)
	for _, a := range active {
		activeMap[a.Name] = true
	}

	perPage := 5
	total := len(all)
	start := page * perPage
	end := start + perPage
	if end > total {
		end = total
	}

	var sb strings.Builder
	sb.WriteString(fmt.Sprintf("<b>Skills:</b> <code>%d total</code>\n\n", total))

	for i := start; i < end; i++ {
		s := all[i]
		status := "○"
		if activeMap[s.Name] {
			status = "●"
		}
		if s.TriggerMode == "always" {
			status = "★"
		}
		sb.WriteString(fmt.Sprintf("%s <code>%s</code> — <i>%s</i>\n", status, s.Name, s.Description))
	}

	var rows [][]tgbotapi.InlineKeyboardButton
	var currentRow []tgbotapi.InlineKeyboardButton

	if page > 0 {
		currentRow = append(currentRow, tgbotapi.NewInlineKeyboardButtonData("◀ Prev", fmt.Sprintf("page:skills:%d", page-1)))
	}
	if end < total {
		currentRow = append(currentRow, tgbotapi.NewInlineKeyboardButtonData("Next ▶", fmt.Sprintf("page:skills:%d", page+1)))
	}
	if len(currentRow) > 0 {
		rows = append(rows, currentRow)
	}

	var keyboard tgbotapi.InlineKeyboardMarkup
	if len(rows) > 0 {
		keyboard = tgbotapi.NewInlineKeyboardMarkup(rows...)
		t.sendMessageWithKeyboardHTML(chatID, sb.String(), keyboard)
	} else {
		t.sendHTML(chatID, sb.String())
	}
}

func (t *TelegramTransport) handleMCPCommand(chatID int64, page int) {
	tools := t.agent.MCP.ListAllTools()
	perPage := 5
	total := len(tools)
	start := page * perPage
	end := start + perPage
	if end > total {
		end = total
	}

	var sb strings.Builder
	sb.WriteString(fmt.Sprintf("<b>MCP tools:</b> <code>%d total</code>\n\n", total))

	if total == 0 {
		sb.WriteString("<i>No servers connected</i>")
	} else {
		for i := start; i < end; i++ {
			tool := tools[i]
			sb.WriteString(fmt.Sprintf("• <code>%s</code> — <i>%s</i>\n", tool.Name, tool.Description))
		}
	}

	var rows [][]tgbotapi.InlineKeyboardButton
	var currentRow []tgbotapi.InlineKeyboardButton

	if page > 0 {
		currentRow = append(currentRow, tgbotapi.NewInlineKeyboardButtonData("◀ Prev", fmt.Sprintf("page:mcp:%d", page-1)))
	}
	if end < total {
		currentRow = append(currentRow, tgbotapi.NewInlineKeyboardButtonData("Next ▶", fmt.Sprintf("page:mcp:%d", page+1)))
	}
	if len(currentRow) > 0 {
		rows = append(rows, currentRow)
	}

	var keyboard tgbotapi.InlineKeyboardMarkup
	if len(rows) > 0 {
		keyboard = tgbotapi.NewInlineKeyboardMarkup(rows...)
		t.sendMessageWithKeyboardHTML(chatID, sb.String(), keyboard)
	} else {
		t.sendHTML(chatID, sb.String())
	}
}

func (t *TelegramTransport) processAgentRequest(chatID int64, text string) {
	log.Printf("[TG] Agent request: chat=%d, text=%q", chatID, text)
	ctx, cancel := context.WithTimeout(context.Background(), 120*time.Second)
	defer cancel()

	thinkMsg, err := t.sendHTML(chatID, "<i>Thinking...</i>")
	if err != nil {
		log.Printf("[TG] ERROR send thinking: %v", err)
		return
	}
	log.Printf("[TG] Thinking msg=%d", thinkMsg.MessageID)

	result, err := t.agent.Run(ctx, chatID, text)
	if err != nil {
		log.Printf("[TG] ERROR agent run: %v", err)
		t.editHTML(chatID, thinkMsg.MessageID, "<b>Error:</b> "+html.EscapeString(err.Error()))
		return
	}

	log.Printf("[TG] Agent result: type=%s, tokens=%d, answer=%d chars", result.AgentType, result.TotalTokens, len(result.Answer))

	response := formatToHTML(result.Answer)

	if len(response) > 500 {
		response = "<blockquote expandable>\n" + response + "\n</blockquote>"
	}

	tokenInfo := fmt.Sprintf("<i>Tokens: in %s out %s total %s</i>",
		formatNum(result.InputTokens),
		formatNum(result.OutputTokens),
		formatNum(result.TotalTokens))

	finalText := response + "\n\n" + tokenInfo

	keyboard := tgbotapi.NewInlineKeyboardMarkup(
		tgbotapi.NewInlineKeyboardRow(
			tgbotapi.NewInlineKeyboardButtonData("Clear", fmt.Sprintf("action:clear:%d", chatID)),
			tgbotapi.NewInlineKeyboardButtonData("Regen", fmt.Sprintf("action:regen:%s", text)),
		),
	)

	t.editMessageWithKeyboardHTML(chatID, thinkMsg.MessageID, finalText, keyboard)
	log.Printf("[TG] Final message edited, done")
}

func (t *TelegramTransport) handleCallback(query *tgbotapi.CallbackQuery) {
	chatID := query.Message.Chat.ID
	msgID := query.Message.MessageID
	data := query.Data
	log.Printf("[TG] Callback: chat=%d data=%q", chatID, data)

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
			t.editHTML(chatID, msgID, query.Message.Text)
		}

	case "action":
		switch parts[1] {
		case "clear":
			t.agent.Memory.ClearHistory(chatID)
			t.editHTML(chatID, msgID, "<b>Memory cleared!</b>")
		case "regen":
			if len(parts) >= 3 {
				originalText := strings.Join(parts[2:], ":")
				t.bot.Request(tgbotapi.NewDeleteMessage(chatID, msgID))
				t.processAgentRequest(chatID, originalText)
			}
		}

	case "page":
		if len(parts) >= 3 {
			page, _ := strconv.Atoi(parts[2])
			switch parts[1] {
			case "skills":
				t.handleSkillsCommand(chatID, page)
			case "mcp":
				t.handleMCPCommand(chatID, page)
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

	argsStr := formatArgsHTML(args)
	text := fmt.Sprintf("<b>Confirm action:</b>\n\n<b>Tool:</b> <code>%s</code>\n<b>Args:</b>\n%s\n\nExecute?", toolName, argsStr)

	keyboard := tgbotapi.NewInlineKeyboardMarkup(
		tgbotapi.NewInlineKeyboardRow(
			tgbotapi.NewInlineKeyboardButtonData("Yes", fmt.Sprintf("confirm:yes:%s", token)),
			tgbotapi.NewInlineKeyboardButtonData("No", fmt.Sprintf("confirm:no:%s", token)),
		),
	)

	t.editMessageWithKeyboardHTML(chatID, msgID, text, keyboard)

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

func formatArgsHTML(args map[string]interface{}) string {
	var parts []string
	for k, v := range args {
		parts = append(parts, fmt.Sprintf("  - <b>%s:</b> <code>%v</code>", k, v))
	}
	return strings.Join(parts, "\n")
}

func (t *TelegramTransport) SendFileBytes(chatID int64, name string, data []byte, caption string) error {
	log.Printf("[TG] Sending file: %s (%d bytes) to chat=%d", name, len(data), chatID)

	file := tgbotapi.NewDocument(chatID, tgbotapi.FileBytes{Name: name, Bytes: data})
	if caption != "" {
		file.Caption = caption
		file.ParseMode = tgbotapi.ModeHTML
	}

	sentMsg, err := t.bot.Send(file)
	if err != nil {
		log.Printf("[TG] ERROR SendFileBytes: %v", err)
		return err
	}

	log.Printf("[TG] File sent: msg_id=%d, file=%s", sentMsg.MessageID, name)
	return nil
}

func (t *TelegramTransport) sendHTML(chatID int64, text string) (tgbotapi.Message, error) {
	msg := tgbotapi.NewMessage(chatID, text)
	msg.ParseMode = tgbotapi.ModeHTML
	return t.bot.Send(msg)
}

func (t *TelegramTransport) sendMessageWithKeyboardHTML(chatID int64, text string, keyboard tgbotapi.InlineKeyboardMarkup) (tgbotapi.Message, error) {
	msg := tgbotapi.NewMessage(chatID, text)
	msg.ParseMode = tgbotapi.ModeHTML
	msg.ReplyMarkup = keyboard
	return t.bot.Send(msg)
}

func (t *TelegramTransport) editHTML(chatID int64, msgID int, text string) {
	edit := tgbotapi.NewEditMessageText(chatID, msgID, text)
	edit.ParseMode = tgbotapi.ModeHTML
	t.bot.Request(edit)
}

func (t *TelegramTransport) editMessageWithKeyboardHTML(chatID int64, msgID int, text string, keyboard tgbotapi.InlineKeyboardMarkup) {
	edit := tgbotapi.NewEditMessageText(chatID, msgID, text)
	edit.ParseMode = tgbotapi.ModeHTML
	edit.ReplyMarkup = &keyboard
	t.bot.Request(edit)
}

func formatToHTML(text string) string {
	text = html.EscapeString(text)

	for strings.Contains(text, "**") {
		idx := strings.Index(text, "**")
		if idx == -1 {
			break
		}
		endIdx := strings.Index(text[idx+2:], "**")
		if endIdx == -1 {
			break
		}
		endIdx += idx + 2
		inner := text[idx+2 : endIdx]
		text = text[:idx] + "<b>" + inner + "</b>" + text[endIdx+2:]
	}

	for strings.Contains(text, "*") {
		idx := strings.Index(text, "*")
		if idx == -1 || idx+1 >= len(text) {
			break
		}
		if text[idx+1] == '*' {
			continue
		}
		endIdx := strings.Index(text[idx+1:], "*")
		if endIdx == -1 {
			break
		}
		endIdx += idx + 1
		inner := text[idx+1 : endIdx]
		text = text[:idx] + "<i>" + inner + "</i>" + text[endIdx+1:]
	}

	for strings.Contains(text, "`") {
		idx := strings.Index(text, "`")
		if idx == -1 {
			break
		}
		if idx+1 < len(text) && text[idx+1] == '`' {
			continue
		}
		endIdx := strings.Index(text[idx+1:], "`")
		if endIdx == -1 {
			break
		}
		endIdx += idx + 1
		inner := text[idx+1 : endIdx]
		text = text[:idx] + "<code>" + inner + "</code>" + text[endIdx+1:]
	}

	for strings.Contains(text, "```") {
		idx := strings.Index(text, "```")
		if idx == -1 {
			break
		}
		endIdx := strings.Index(text[idx+3:], "```")
		if endIdx == -1 {
			break
		}
		endIdx += idx + 3
		inner := text[idx+3 : endIdx]
		if nl := strings.Index(inner, "\n"); nl > 0 && nl < 20 {
			inner = inner[nl+1:]
		}
		text = text[:idx] + "<pre><code>" + inner + "</code></pre>" + text[endIdx+3:]
	}

	return text
}

func formatNum(n int) string {
	if n >= 1000 {
		return fmt.Sprintf("%.1fk", float64(n)/1000)
	}
	return fmt.Sprintf("%d", n)
}
t64(n)/1000)
	}
	return fmt.Sprintf("%d", n)
}
