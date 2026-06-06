package agent

import (
	"encoding/json"
	"regexp"
	"strings"
)

type ToolCall struct {
	Tool string                 `json:"tool"`
	Args map[string]interface{} `json:"args"`
	Body string                 `json:"body,omitempty"`
}

func ParseToolCalls(text string) []ToolCall {
	var calls []ToolCall
	re := regexp.MustCompile("```tool_call\\s*\\n?([\\s\\S]*?)```")
	matches := re.FindAllStringSubmatch(text, -1)

	for _, match := range matches {
		if len(match) < 2 {
			continue
		}
		content := strings.TrimSpace(match[1])
		var call ToolCall
		if err := json.Unmarshal([]byte(content), &call); err == nil {
			calls = append(calls, call)
		}
	}
	return calls
}

func HasToolCalls(text string) bool {
	return len(ParseToolCalls(text)) > 0
}

func StripToolCalls(text string) string {
	re := regexp.MustCompile("```tool_call\\s*\\n?[\\s\\S]*?```")
	return strings.TrimSpace(re.ReplaceAllString(text, ""))
}
