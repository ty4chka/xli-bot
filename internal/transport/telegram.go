
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

const (
	MaxMessageLength = 4000
	PageSize         = 3500
)

type TelegramTransport struct {
	bot           *tgbotapi.BotAPI
	agent         *agent.Agent
	confirmations map[string]chan bool
	mu            sync.RWMutex
	bookPages     map[string][]string
	bookMu        sync.RWMutex
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
		bookPages:     make(map[string][]string),
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
		t.handleSkillsCommand(chatID, 0, 0)

	case "mcp":
		t.handleMCPCommand(chatID, 0, 0)

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

func (t *TelegramTransport) handleSkillsCommand(chatID int64, page int, msgID int) {
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
		sb.WriteString(fmt.Sprintf("%s <code>%s</code> — <i>%s</i>\n", status, s.Name, s.TriggerMode))
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

	if msgID == 0 {
		if len(rows) > 0 {
			keyboard := tgbotapi.NewInlineKeyboardMarkup(rows...)
			t.sendMessageWithKeyboardHTML(chatID, sb.String(), keyboard)
		} else {
			t.sendHTML(chatID, sb.String())
		}
	} else {
		edit := tgbotapi.NewEditMessageText(chatID, msgID, sb.String())
		edit.ParseMode = tgbotapi.ModeHTML
		if len(rows) > 0 {
			keyboard := tgbotapi.NewInlineKeyboardMarkup(rows...)
			edit.ReplyMarkup = &keyboard
		}
		t.bot.Request(edit)
	}
}

func (t *TelegramTransport) handleMCPCommand(chatID int64, page int, msgID int) {
	status := t.agent.MCP.Status()

	if msgID == 0 {
		t.sendHTML(chatID, status)
	} else {
		edit := tgbotapi.NewEditMessageText(chatID, msgID, status)
		edit.ParseMode = tgbotapi.ModeHTML
		t.bot.Request(edit)
	}
}

func (t *TelegramTransport) processAgentRequest(chatID int64, text string) {
	log.Printf("[TG] Agent request: chat=%d, text=%q", chatID, text)

	// Dynamic timeout: 300s for heavy tasks, 120s default
	timeout := 120 * time.Second
	lower := strings.ToLower(text)
	if strings.Contains(lower, "project") || strings.Contains(lower, "compile") ||
		strings.Contains(lower, "build") || strings.Contains(lower, "архив") ||
		strings.Contains(lower, "archive") || len(text) > 200 {
		timeout = 300 * time.Second
	}

	ctx, cancel := context.WithTimeout(context.Background(), timeout)
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

	log.Printf("[TG] Agent result: type=%s, tokens=%d, answer=%d chars",
		result.AgentType, result.TotalTokens, len(result.Answer))

	response := formatToHTML(result.Answer)

	// Truncate if too long
	if len(response) > MaxMessageLength-200 {
		response = response[:MaxMessageLength-200] + "\n\n<i>... truncated</i>"
	}

	tokenInfo := fmt.Sprintf("<i>Tokens: in %s out %s total %s</i>",
		formatNum(result.InputTokens),
		formatNum(result.OutputTokens),
		formatNum(result.TotalTokens))

	fullText := response + "\n\n" + tokenInfo

	if len(fullText) <= MaxMessageLength {
		// Build action buttons based on result
		keyboard := t.buildActionKeyboard(chatID, text, result)
		if err := t.editMessageWithKeyboardHTML(chatID, thinkMsg.MessageID, fullText, keyboard); err != nil {
			log.Printf("[TG] ERROR editing short answer: %v", err)
			// Fallback: send new message
			t.sendMessageWithKeyboardHTML(chatID, fullText, keyboard)
		}
		log.Printf("[TG] Short answer edited, done")
		return
	}

	pages := t.splitIntoPages(response, PageSize)
	totalPages := len(pages)
	log.Printf("[TG] Book mode: %d pages", totalPages)

	bookKey := fmt.Sprintf("%d_%d", chatID, thinkMsg.MessageID)
	t.bookMu.Lock()
	t.bookPages[bookKey] = pages
	t.bookMu.Unlock()

	firstPage := pages[0] + fmt.Sprintf("\n\n📄 <i>Page 1/%d</i>\n%s", totalPages, tokenInfo)
	keyboard := t.buildBookKeyboard(bookKey, 0, totalPages, chatID, text)
	t.editMessageWithKeyboardHTML(chatID, thinkMsg.MessageID, firstPage, keyboard)
	log.Printf("[TG] Book page 1/%d shown", totalPages)
}

// buildActionKeyboard — кнопки под результатом: [Run] [Download] [Explain] [Clear] [Regen]
func (t *TelegramTransport) buildActionKeyboard(chatID int64, originalText string, result *agent.AgentResult) tgbotapi.InlineKeyboardMarkup {
	var rows [][]tgbotapi.InlineKeyboardButton

	// Row 1: Action buttons for code results
	var actionRow []tgbotapi.InlineKeyboardButton
	if result.AgentType == "tier2_single" || result.AgentType == "coder" || result.AgentType == "build" {
		actionRow = append(actionRow, tgbotapi.NewInlineKeyboardButtonData("▶️ Run", fmt.Sprintf("action:run:%d", chatID)))
		actionRow = append(actionRow, tgbotapi.NewInlineKeyboardButtonData("📦 Download", fmt.Sprintf("action:download:%d", chatID)))
		actionRow = append(actionRow, tgbotapi.NewInlineKeyboardButtonData("📖 Explain", fmt.Sprintf("action:explain:%s", originalText)))
	}
	if len(actionRow) > 0 {
		rows = append(rows, actionRow)
	}

	// Row 2: Utility buttons
	rows = append(rows, tgbotapi.NewInlineKeyboardRow(
		tgbotapi.NewInlineKeyboardButtonData("🧹 Clear", fmt.Sprintf("action:clear:%d", chatID)),
		tgbotapi.NewInlineKeyboardButtonData("🔃 Regen", fmt.Sprintf("action:regen:%s", originalText)),
	))

	return tgbotapi.NewInlineKeyboardMarkup(rows...)
}

func (t *TelegramTransport) buildBookKeyboard(bookKey string, currentPage, totalPages int, chatID int64, originalText string) tgbotapi.InlineKeyboardMarkup {
	var rows [][]tgbotapi.InlineKeyboardButton

	var navRow []tgbotapi.InlineKeyboardButton
	if currentPage > 0 {
		navRow = append(navRow, tgbotapi.NewInlineKeyboardButtonData("◀ Prev", fmt.Sprintf("book|%s|%d", bookKey, currentPage-1)))
	}
	navRow = append(navRow, tgbotapi.NewInlineKeyboardButtonData(fmt.Sprintf("%d/%d", currentPage+1, totalPages), "noop"))
	if currentPage < totalPages-1 {
		navRow = append(navRow, tgbotapi.NewInlineKeyboardButtonData("Next ▶", fmt.Sprintf("book|%s|%d", bookKey, currentPage+1)))
	}
	if len(navRow) > 0 {
		rows = append(rows, navRow)
	}

	rows = append(rows, tgbotapi.NewInlineKeyboardRow(
		tgbotapi.NewInlineKeyboardButtonData("🧹 Clear", fmt.Sprintf("action:clear:%d", chatID)),
		tgbotapi.NewInlineKeyboardButtonData("🔃 Regen", fmt.Sprintf("action:regen:%s", originalText)),
	))

	return tgbotapi.NewInlineKeyboardMarkup(rows...)
}

func (t *TelegramTransport) splitIntoPages(text string, maxLen int) []string {
	var pages []string
	var currentPage strings.Builder

	paragraphs := strings.Split(text, "\n\n")

	for _, para := range paragraphs {
		if len(para) > maxLen {
			lines := strings.Split(para, "\n")
			for _, line := range lines {
				if currentPage.Len()+len(line)+1 > maxLen {
					if currentPage.Len() > 0 {
						pages = append(pages, currentPage.String())
						currentPage.Reset()
					}
				}
				if currentPage.Len() > 0 {
					currentPage.WriteString("\n")
				}
				currentPage.WriteString(line)
			}
			continue
		}

		if currentPage.Len()+len(para)+2 > maxLen {
			if currentPage.Len() > 0 {
				pages = append(pages, currentPage.String())
				currentPage.Reset()
			}
		}

		if currentPage.Len() > 0 {
			currentPage.WriteString("\n\n")
		}
		currentPage.WriteString(para)
	}

	if currentPage.Len() > 0 {
		pages = append(pages, currentPage.String())
	}

	return pages
}

func (t *TelegramTransport) handleCallback(query *tgbotapi.CallbackQuery) {
	chatID := query.Message.Chat.ID
	msgID := query.Message.MessageID
	data := query.Data
	log.Printf("[TG] Callback: chat=%d data=%q", chatID, data)

	// Book pagination
	if strings.HasPrefix(data, "book|") {
		parts := strings.Split(data, "|")
		if len(parts) >= 3 {
			bookKey := parts[1]
			pageNum, _ := strconv.Atoi(parts[2])

			t.bookMu.RLock()
			pages, ok := t.bookPages[bookKey]
			t.bookMu.RUnlock()

			if ok && pageNum >= 0 && pageNum < len(pages) {
				keyboard := t.buildBookKeyboard(bookKey, pageNum, len(pages), chatID, "")
				pageText := pages[pageNum] + fmt.Sprintf("\n\n📄 <i>Page %d/%d</i>", pageNum+1, len(pages))
				t.editMessageWithKeyboardHTML(chatID, msgID, pageText, keyboard)
			}
		}
		t.bot.Request(tgbotapi.NewCallback(query.ID, ""))
		return
	}

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
		if len(parts) < 2 {
			break
		}
		switch parts[1] {
		case "clear":
			if len(parts) >= 3 {
				cid, _ := strconv.ParseInt(parts[2], 10, 64)
				t.agent.Memory.ClearHistory(cid)
				t.editHTML(chatID, msgID, "<b>Memory cleared!</b>")
			}
		case "regen":
			if len(parts) >= 3 {
				originalText := strings.Join(parts[2:], ":")
				t.bot.Request(tgbotapi.NewDeleteMessage(chatID, msgID))
				t.processAgentRequest(chatID, originalText)
			}
		case "run":
			// Placeholder: run last code
			t.bot.Request(tgbotapi.NewCallback(query.ID, "Run triggered"))
			return
		case "download":
			// Placeholder: download last archive
			t.bot.Request(tgbotapi.NewCallback(query.ID, "Download triggered"))
			return
		case "explain":
			if len(parts) >= 3 {
				originalText := strings.Join(parts[2:], ":")
				explainQuery := "Explain this code in detail: " + originalText
				t.bot.Request(tgbotapi.NewDeleteMessage(chatID, msgID))
				t.processAgentRequest(chatID, explainQuery)
			}
		}

	case "page":
		if len(parts) >= 3 {
			page, _ := strconv.Atoi(parts[2])
			switch parts[1] {
			case "skills":
				t.handleSkillsCommand(chatID, page, msgID)
			case "mcp":
				t.handleMCPCommand(chatID, page, msgID)
			}
		}

	case "noop":
		t.bot.Request(tgbotapi.NewCallback(query.ID, ""))
		return
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

	fileBytes := tgbotapi.FileBytes{
		Name:  name,
		Bytes: data,
	}

	file := tgbotapi.NewDocument(chatID, fileBytes)
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
	if _, err := t.bot.Request(edit); err != nil {
		log.Printf("[TG] editHTML error: %v", err)
	}
}

func (t *TelegramTransport) editMessageWithKeyboardHTML(chatID int64, msgID int, text string, keyboard tgbotapi.InlineKeyboardMarkup) error {
	edit := tgbotapi.NewEditMessageText(chatID, msgID, text)
	edit.ParseMode = tgbotapi.ModeHTML
	edit.ReplyMarkup = &keyboard
	resp, err := t.bot.Request(edit)
	if err != nil {
		log.Printf("[TG] editMessageWithKeyboardHTML error: %v, resp=%v", err, resp)
		return err
	}
	return nil
}

func formatToHTML(text string) string {
	lines := strings.Split(text, "\n")
	var result []string
	inQuote := false

	for _, line := range lines {
		trimmed := strings.TrimLeft(line, " ")
		if strings.HasPrefix(trimmed, "> ") || strings.HasPrefix(trimmed, ">>") {
			if !inQuote {
				result = append(result, "<blockquote expandable>")
				inQuote = true
			}
			quoteText := strings.TrimPrefix(trimmed, "> ")
			quoteText = strings.TrimPrefix(quoteText, ">>")
			result = append(result, html.EscapeString(quoteText))
		} else {
			if inQuote {
				result = append(result, "</blockquote>")
				inQuote = false
			}
			result = append(result, html.EscapeString(line))
		}
	}
	if inQuote {
		result = append(result, "</blockquote>")
	}

	text = strings.Join(result, "\n")

	// Bold **text**
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

	// Italic *text*
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

	// Code `text`
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

	// Code blocks ```text```
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
