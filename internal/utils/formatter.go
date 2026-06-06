package utils

import (
	"fmt"
	"strings"

	tgbotapi "github.com/go-telegram-bot-api/telegram-bot-api/v5"
)

func EscapeMarkdownV2(text string) string {
	chars := []string{"_", "*", "[", "]", "(", ")", "~", "`", ">", "#", "+", "-", "=", "|", "{", "}", ".", "!"}
	for _, char := range chars {
		text = strings.ReplaceAll(text, char, "\"+char)
	}
	return text
}

func FormatResponse(text string) string {
	text = strings.TrimSpace(text)

	// Bold **text** → *text*
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
		text = text[:idx] + "*" + inner + "*" + text[endIdx+2:]
	}

	// Code blocks
	text = strings.ReplaceAll(text, "```", "```")

	return text
}

func SendFormatted(bot *tgbotapi.BotAPI, chatID int64, text string) (tgbotapi.Message, error) {
	const maxLen = 4000
	if len(text) > maxLen {
		parts := splitMessage(text, maxLen)
		var lastMsg tgbotapi.Message
		for _, part := range parts {
			msg := tgbotapi.NewMessage(chatID, part)
			msg.ParseMode = tgbotapi.ModeMarkdownV2
			lastMsg, _ = bot.Send(msg)
		}
		return lastMsg, nil
	}

	msg := tgbotapi.NewMessage(chatID, text)
	msg.ParseMode = tgbotapi.ModeMarkdownV2
	return bot.Send(msg)
}

func EditFormatted(bot *tgbotapi.BotAPI, chatID int64, msgID int, text string) error {
	const maxLen = 4000
	if len(text) > maxLen {
		text = text[:maxLen-3] + "..."
	}

	edit := tgbotapi.NewEditMessageText(chatID, msgID, text)
	edit.ParseMode = tgbotapi.ModeMarkdownV2
	_, err := bot.Request(edit)
	return err
}

func splitMessage(text string, maxLen int) []string {
	var parts []string
	for len(text) > maxLen {
		idx := strings.LastIndex(text[:maxLen], "
")
		if idx == -1 {
			idx = maxLen
		}
		parts = append(parts, text[:idx])
		text = text[idx:]
	}
	if len(text) > 0 {
		parts = append(parts, text)
	}
	return parts
}

func FormatTokenUsage(input, output, total int) string {
	return "💸 *Токены:* in " + formatNum(input) + " · out " + formatNum(output) + " · total " + formatNum(total)
}

func formatNum(n int) string {
	if n >= 1000 {
		return fmt.Sprintf("%.1fk", float64(n)/1000)
	}
	return fmt.Sprintf("%d", n)
}
