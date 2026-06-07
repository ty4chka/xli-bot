package sandbox

import (
	"context"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"
)

// Sandbox — изолированная среда выполнения
type Sandbox struct {
	WorkDir    string
	MaxTime    time.Duration
	MaxMemory  int64 // MB
	MaxOutput  int64 // bytes
	AllowedCmds []string
}

func NewSandbox(workDir string) (*Sandbox, error) {
	if err := os.MkdirAll(workDir, 0755); err != nil {
		return nil, err
	}

	return &Sandbox{
		WorkDir:    workDir,
		MaxTime:    30 * time.Second,
		MaxMemory:  128, // MB
		MaxOutput:  1024 * 1024, // 1MB
		AllowedCmds: []string{"go", "python3", "sh", "echo", "cat", "ls", "grep", "sed", "awk"},
	}, nil
}

// RunCommand — выполняет команду в песочнице
func (s *Sandbox) RunCommand(ctx context.Context, cmd string) (string, error) {
	// Проверяем, разрешена ли команда
	parts := strings.Fields(cmd)
	if len(parts) == 0 {
		return "", fmt.Errorf("empty command")
	}
	if !s.isAllowed(parts[0]) {
		return "", fmt.Errorf("command not allowed: %s", parts[0])
	}

	ctx, cancel := context.WithTimeout(ctx, s.MaxTime)
	defer cancel()

	// Запускаем в chroot-like окружении (простая версия)
	execCmd := exec.CommandContext(ctx, "sh", "-c", cmd)
	execCmd.Dir = s.WorkDir

	// Лимиты ресурсов (Linux-only)
	// execCmd.SysProcAttr = &syscall.SysProcAttr{
	//     Setrlimit: []syscall.Rlimit{
	//         {Type: syscall.RLIMIT_AS, Cur: s.MaxMemory * 1024 * 1024, Max: s.MaxMemory * 1024 * 1024},
	//     },
	// }

	output, err := execCmd.CombinedOutput()
	if ctx.Err() == context.DeadlineExceeded {
		return string(output), fmt.Errorf("timeout after %v", s.MaxTime)
	}

	// Ограничиваем вывод
	if int64(len(output)) > s.MaxOutput {
		output = output[:s.MaxOutput]
	}

	return string(output), err
}

// WriteFile — записывает файл в песочницу
func (s *Sandbox) WriteFile(name string, content []byte) error {
	path := filepath.Join(s.WorkDir, name)
	dir := filepath.Dir(path)
	if err := os.MkdirAll(dir, 0755); err != nil {
		return err
	}
	return os.WriteFile(path, content, 0644)
}

// ReadFile — читает файл из песочницы
func (s *Sandbox) ReadFile(name string) ([]byte, error) {
	path := filepath.Join(s.WorkDir, name)
	return os.ReadFile(path)
}

// Cleanup — удаляет всё из песочницы
func (s *Sandbox) Cleanup() error {
	return os.RemoveAll(s.WorkDir)
}

func (s *Sandbox) isAllowed(cmd string) bool {
	for _, allowed := range s.AllowedCmds {
		if allowed == cmd {
			return true
		}
	}
	return false
}

// GoBuild — компилирует Go в песочнице
func (s *Sandbox) GoBuild(sourceFile string) (string, error) {
	cmd := fmt.Sprintf("cd %s && go build -o %s %s",
		s.WorkDir,
		strings.TrimSuffix(filepath.Base(sourceFile), ".go"),
		sourceFile)
	return s.RunCommand(context.Background(), cmd)
}

// GoRun — запускает Go в песочнице
func (s *Sandbox) GoRun(sourceFile string) (string, error) {
	cmd := fmt.Sprintf("cd %s && go run %s", s.WorkDir, sourceFile)
	return s.RunCommand(context.Background(), cmd)
}

// PythonRun — запускает Python в песочнице
func (s *Sandbox) PythonRun(script string) (string, error) {
	cmd := fmt.Sprintf("cd %s && python3 %s", s.WorkDir, script)
	return s.RunCommand(context.Background(), cmd)
}
