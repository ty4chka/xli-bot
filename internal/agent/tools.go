package agent

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"

	"github.com/oblachko/xli-bot/internal/utils"
)

type ToolExecutor struct {
	transport Transport
}

type Transport interface {
	ShowConfirmation(chatID int64, msgID int, toolName string, args map[string]interface{}) (bool, error)
	SendFileBytes(chatID int64, name string, data []byte, caption string) error
}

func NewToolExecutor(t Transport) *ToolExecutor {
	return &ToolExecutor{transport: t}
}

func (te *ToolExecutor) Execute(ctx context.Context, chatID int64, msgID int, call ToolCall) (string, error) {
	switch call.Tool {
	case "thinking.note":
		return te.thinkingNote(call.Args)
	case "terminal.run":
		return te.terminalRun(ctx, chatID, msgID, call.Args)
	case "file.read":
		return te.fileRead(call.Args)
	case "file.write":
		return te.fileWrite(ctx, chatID, msgID, call.Args)
	case "web.search":
		return te.webSearch(ctx, call.Args)
	case "web.fetch":
		return te.webFetch(ctx, call.Args)
	case "github.build":
		return te.githubBuild(ctx, chatID, msgID, call.Args)
	default:
		return "", fmt.Errorf("unknown tool: %s", call.Tool)
	}
}

func (te *ToolExecutor) thinkingNote(args map[string]interface{}) (string, error) {
	note, _ := args["note"].(string)
	return fmt.Sprintf("Thinking: %s", note), nil
}

func (te *ToolExecutor) terminalRun(ctx context.Context, chatID int64, msgID int, args map[string]interface{}) (string, error) {
	cmdStr, _ := args["cmd"].(string)
	if cmdStr == "" {
		return "", fmt.Errorf("no command")
	}

	if isDangerous(cmdStr) {
		approved, err := te.transport.ShowConfirmation(chatID, msgID, "terminal.run", args)
		if err != nil || !approved {
			return "Cancelled by user", nil
		}
	}

	ctx, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()

	cmd := exec.CommandContext(ctx, "sh", "-c", cmdStr)
	output, err := cmd.CombinedOutput()
	if err != nil {
		return fmt.Sprintf("Error: %v\n%s", err, string(output)), nil
	}
	return string(output), nil
}

func (te *ToolExecutor) fileRead(args map[string]interface{}) (string, error) {
	path, _ := args["path"].(string)
	if path == "" {
		return "", fmt.Errorf("no path")
	}
	data, err := os.ReadFile(path)
	if err != nil {
		return "", err
	}
	return string(data), nil
}

func (te *ToolExecutor) fileWrite(ctx context.Context, chatID int64, msgID int, args map[string]interface{}) (string, error) {
	path, _ := args["path"].(string)
	content, _ := args["content"].(string)
	if path == "" {
		return "", fmt.Errorf("no path")
	}

	if _, err := os.Stat(path); err == nil {
		approved, err := te.transport.ShowConfirmation(chatID, msgID, "file.write", args)
		if err != nil || !approved {
			return "Cancelled by user", nil
		}
	}

	err := os.WriteFile(path, []byte(content), 0644)
	if err != nil {
		return "", err
	}

	fileName := filepath.Base(path)
	data := []byte(content)

	// FIX: Отправляем файл с проверкой ошибки
	if err := te.transport.SendFileBytes(chatID, fileName, data,
		fmt.Sprintf("File: <code>%s</code> (%d bytes)", fileName, len(data))); err != nil {
		return fmt.Sprintf("Written: %s (%d bytes) [send error: %v]", path, len(data), err), nil
	}

	// FIX: Авто-компиляция Go
	if strings.HasSuffix(path, ".go") {
		dir := filepath.Dir(path)
		binName := strings.TrimSuffix(fileName, ".go")

		var compileCmd string
		if dir == "." || dir == "" {
			compileCmd = fmt.Sprintf("go build -o %s %s", binName, fileName)
		} else {
			compileCmd = fmt.Sprintf("cd %s && go build -o %s %s", dir, binName, fileName)
		}

		cmd := exec.CommandContext(ctx, "sh", "-c", compileCmd)
		compileOutput, compileErr := cmd.CombinedOutput()
		if compileErr != nil {
			return fmt.Sprintf("Written: %s (%d bytes)\nCompile error: %v\n%s", path, len(data), compileErr, string(compileOutput)), nil
		}

		// Отправляем скомпилированный бинарник
		binPath := filepath.Join(dir, binName)
		if binData, err := os.ReadFile(binPath); err == nil {
			te.transport.SendFileBytes(chatID, binName, binData,
				fmt.Sprintf("Compiled: <code>%s</code>", binName))
		}

		return fmt.Sprintf("Written: %s (%d bytes)\nCompiled: %s\n%s", path, len(data), binName, string(compileOutput)), nil
	}

	return fmt.Sprintf("Written: %s (%d bytes)", path, len(data)), nil
}

func (te *ToolExecutor) webSearch(ctx context.Context, args map[string]interface{}) (string, error) {
	query, _ := args["query"].(string)
	if query == "" {
		return "", fmt.Errorf("no query")
	}
	results, err := utils.WebSearch(ctx, query)
	if err != nil {
		return "", err
	}
	return utils.FormatSearchResults(results, query), nil
}

func (te *ToolExecutor) webFetch(ctx context.Context, args map[string]interface{}) (string, error) {
	urlStr, _ := args["url"].(string)
	if urlStr == "" {
		return "", fmt.Errorf("no URL")
	}
	return utils.WebFetch(ctx, urlStr)
}

func (te *ToolExecutor) githubBuild(ctx context.Context, chatID int64, msgID int, args map[string]interface{}) (string, error) {
	approved, err := te.transport.ShowConfirmation(chatID, msgID, "github.build", args)
	if err != nil || !approved {
		return "Cancelled by user", nil
	}
	return "GitHub Actions dispatched! (stub)", nil
}

func isDangerous(cmd string) bool {
	dangerous := []string{"rm -rf /", "mkfs", "dd if=", "> /dev/sd", ":(){:|:&};:"}
	lower := strings.ToLower(cmd)
	for _, d := range dangerous {
		if strings.Contains(lower, d) {
			return true
		}
	}
	return false
}
