package agent

import (
	"context"
	"fmt"
	"log"
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
	log.Printf("[TOOL] Executing: %s", call.Tool)

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
	log.Printf("[TOOL] thinking.note: %s", note)
	return fmt.Sprintf("Thinking: %s", note), nil
}

func (te *ToolExecutor) terminalRun(ctx context.Context, chatID int64, msgID int, args map[string]interface{}) (string, error) {
	cmdStr, _ := args["cmd"].(string)
	if cmdStr == "" {
		return "", fmt.Errorf("no command")
	}

	log.Printf("[TOOL] terminal.run: %s", cmdStr)

	if isDangerous(cmdStr) {
		log.Printf("[TOOL] Dangerous command, requesting confirmation")
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
		log.Printf("[TOOL] terminal.run error: %v, output: %d chars", err, len(output))
		return fmt.Sprintf("Error: %v\n%s", err, string(output)), nil
	}
	log.Printf("[TOOL] terminal.run success: %d chars", len(output))
	return string(output), nil
}

func (te *ToolExecutor) fileRead(args map[string]interface{}) (string, error) {
	path, _ := args["path"].(string)
	if path == "" {
		return "", fmt.Errorf("no path")
	}
	log.Printf("[TOOL] file.read: %s", path)
	data, err := os.ReadFile(path)
	if err != nil {
		return "", err
	}
	log.Printf("[TOOL] file.read success: %d bytes", len(data))
	return string(data), nil
}

func (te *ToolExecutor) fileWrite(ctx context.Context, chatID int64, msgID int, args map[string]interface{}) (string, error) {
	path, _ := args["path"].(string)
	content, _ := args["content"].(string)
	if path == "" {
		return "", fmt.Errorf("no path")
	}

	log.Printf("[TOOL] file.write: %s (%d bytes)", path, len(content))

	if _, err := os.Stat(path); err == nil {
		log.Printf("[TOOL] File exists, requesting confirmation")
		approved, err := te.transport.ShowConfirmation(chatID, msgID, "file.write", args)
		if err != nil || !approved {
			return "Cancelled by user", nil
		}
	}

	err := os.WriteFile(path, []byte(content), 0644)
	if err != nil {
		log.Printf("[TOOL] file.write error: %v", err)
		return "", err
	}
	log.Printf("[TOOL] file.write success: %s", path)

	fileName := filepath.Base(path)
	data := []byte(content)

	// Отправляем исходник
	log.Printf("[TOOL] Sending source file: %s", fileName)
	if err := te.transport.SendFileBytes(chatID, fileName, data,
		fmt.Sprintf("📄 <code>%s</code> (%d bytes)", fileName, len(data))); err != nil {
		log.Printf("[TOOL] Send source error: %v", err)
	}

	// Авто-компиляция для .go
	if strings.HasSuffix(path, ".go") {
		log.Printf("[TOOL] Auto-compiling Go: %s", path)
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
			log.Printf("[TOOL] Compile error: %v\n%s", compileErr, string(compileOutput))
			return fmt.Sprintf("Written: %s (%d bytes)\n❌ Compile error: %v\n%s",
				path, len(data), compileErr, string(compileOutput)), nil
		}

		log.Printf("[TOOL] Compiled: %s", binName)

		// Отправляем бинарник
		binPath := filepath.Join(dir, binName)
		if binData, err := os.ReadFile(binPath); err == nil {
			log.Printf("[TOOL] Sending binary: %s (%d bytes)", binName, len(binData))
			if sendErr := te.transport.SendFileBytes(chatID, binName, binData,
				fmt.Sprintf("⚙️ Compiled: <code>%s</code> (%d bytes)", binName, len(binData))); sendErr != nil {
				log.Printf("[TOOL] Send binary error: %v", sendErr)
			}
		}

		return fmt.Sprintf("✅ Written: %s (%d bytes)\n✅ Compiled: %s\n%s",
			path, len(data), binName, string(compileOutput)), nil
	}

	// Авто-запуск для .py
	if strings.HasSuffix(path, ".py") {
		log.Printf("[TOOL] Auto-running Python: %s", path)
		runCmd := fmt.Sprintf("python3 %s", path)
		cmd := exec.CommandContext(ctx, "sh", "-c", runCmd)
		runOutput, runErr := cmd.CombinedOutput()
		if runErr != nil {
			log.Printf("[TOOL] Python run error: %v", runErr)
			return fmt.Sprintf("Written: %s (%d bytes)\n❌ Run error: %v\n%s",
				path, len(data), runErr, string(runOutput)), nil
		}
		log.Printf("[TOOL] Python run success: %d chars", len(runOutput))
		return fmt.Sprintf("✅ Written: %s (%d bytes)\n✅ Run output:\n%s",
			path, len(data), string(runOutput)), nil
	}

	return fmt.Sprintf("✅ Written: %s (%d bytes)", path, len(data)), nil
}

func (te *ToolExecutor) webSearch(ctx context.Context, args map[string]interface{}) (string, error) {
	query, _ := args["query"].(string)
	if query == "" {
		return "", fmt.Errorf("no query")
	}
	log.Printf("[TOOL] web.search: %s", query)
	results, err := utils.WebSearch(ctx, query)
	if err != nil {
		return "", err
	}
	log.Printf("[TOOL] web.search: %d results", len(results))
	return utils.FormatSearchResults(results, query), nil
}

func (te *ToolExecutor) webFetch(ctx context.Context, args map[string]interface{}) (string, error) {
	urlStr, _ := args["url"].(string)
	if urlStr == "" {
		return "", fmt.Errorf("no URL")
	}
	log.Printf("[TOOL] web.fetch: %s", urlStr)
	return utils.WebFetch(ctx, urlStr)
}

func (te *ToolExecutor) githubBuild(ctx context.Context, chatID int64, msgID int, args map[string]interface{}) (string, error) {
	log.Printf("[TOOL] github.build")
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
