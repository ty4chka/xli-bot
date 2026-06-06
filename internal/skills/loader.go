// internal/skills/loader.go (горячая + ленивая загрузка)
package skills

import (
	"bufio"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"github.com/fsnotify/fsnotify"
)

type HotLoader struct {
	dir       string
	skills    map[string]*Skill
	active    map[string]bool
	mu        sync.RWMutex
	watcher   *fsnotify.Watcher
	stopWatch chan bool
}

func NewHotLoader() *HotLoader {
	return &HotLoader{
		skills:    make(map[string]*Skill),
		active:    make(map[string]bool),
		stopWatch: make(chan bool),
	}
}

func (h *HotLoader) LoadFromDir(dir string) error {
	h.dir = dir

	// Создаём папку если нет
	os.MkdirAll(dir, 0755)

	// Первичная загрузка
	if err := h.scanDir(); err != nil {
		return err
	}

	// Запускаем watcher для горячей перезагрузки
	return h.startWatcher(dir)
}

func (h *HotLoader) scanDir() error {
	files, err := os.ReadDir(h.dir)
	if err != nil {
		return err
	}

	h.mu.Lock()
	defer h.mu.Unlock()

	for _, file := range files {
		if file.IsDir() || !strings.HasSuffix(file.Name(), ".md") {
			continue
		}

		path := filepath.Join(h.dir, file.Name())
		skill, err := h.parseSkill(path)
		if err != nil {
			continue
		}

		// Проверяем изменился ли файл
		if existing, ok := h.skills[skill.Name]; ok {
			if existing.Modified.Equal(skill.Modified) {
				continue // не изменился, пропускаем
			}
		}

		h.skills[skill.Name] = skill

		// Auto-activate если trigger_mode = always
		if skill.TriggerMode == "always" {
			h.active[skill.Name] = true
		}
	}

	return nil
}

func (h *HotLoader) parseSkill(path string) (*Skill, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}

	info, err := os.Stat(path)
	if err != nil {
		return nil, err
	}

	content := string(data)
	skill := &Skill{
		Source:   path,
		Modified: info.ModTime(),
		Content:  content,
	}

	// Парсим метаданные из YAML frontmatter
	scanner := bufio.NewScanner(strings.NewReader(content))
	inFrontmatter := false
	var frontmatter []string

	for scanner.Scan() {
		line := scanner.Text()
		if line == "---" {
			if !inFrontmatter {
				inFrontmatter = true
				continue
			} else {
				break
			}
		}
		if inFrontmatter {
			frontmatter = append(frontmatter, line)
		}
	}

	// Парсим ключ-значение
	for _, line := range frontmatter {
		parts := strings.SplitN(line, ":", 2)
		if len(parts) != 2 {
			continue
		}
		key := strings.TrimSpace(parts[0])
		val := strings.TrimSpace(parts[1])

		switch key {
		case "name":
			skill.Name = val
		case "description":
			skill.Description = val
		case "trigger_mode":
			skill.TriggerMode = val
		case "keywords":
			skill.Keywords = parseList(val)
		}
	}

	// Если имя не задано — используем имя файла
	if skill.Name == "" {
		skill.Name = strings.TrimSuffix(filepath.Base(path), ".md")
	}
	if skill.TriggerMode == "" {
		skill.TriggerMode = "auto"
	}

	return skill, nil
}

func parseList(s string) []string {
	var result []string
	for _, part := range strings.Split(s, ",") {
		trimmed := strings.TrimSpace(part)
		if trimmed != "" {
			result = append(result, trimmed)
		}
	}
	return result
}

func (h *HotLoader) startWatcher(dir string) error {
	watcher, err := fsnotify.NewWatcher()
	if err != nil {
		return err
	}
	h.watcher = watcher

	if err := watcher.Add(dir); err != nil {
		return err
	}

	go func() {
		for {
			select {
			case event, ok := <-watcher.Events:
				if !ok {
					return
				}
				if strings.HasSuffix(event.Name, ".md") {
					switch {
					case event.Op&fsnotify.Write == fsnotify.Write:
						h.handleFileChange(event.Name, "modified")
					case event.Op&fsnotify.Create == fsnotify.Create:
						h.handleFileChange(event.Name, "created")
					case event.Op&fsnotify.Remove == fsnotify.Remove:
						h.handleFileRemove(event.Name)
					}
				}
			case err, ok := <-watcher.Errors:
				if !ok {
					return
				}
				fmt.Printf("watcher error: %v\n", err)
			case <-h.stopWatch:
				return
			}
		}
	}()

	return nil
}

func (h *HotLoader) handleFileChange(path, action string) {
	h.mu.Lock()
	defer h.mu.Unlock()

	skill, err := h.parseSkill(path)
	if err != nil {
		return
	}

	oldName := ""
	for name, s := range h.skills {
		if s.Source == path {
			oldName = name
			break
		}
	}

	if oldName != "" && oldName != skill.Name {
		delete(h.skills, oldName)
		delete(h.active, oldName)
	}

	h.skills[skill.Name] = skill
	fmt.Printf("🔄 Skill %s: %s\n", action, skill.Name)
}

func (h *HotLoader) handleFileRemove(path string) {
	h.mu.Lock()
	defer h.mu.Unlock()

	for name, skill := range h.skills {
		if skill.Source == path {
			delete(h.skills, name)
			delete(h.active, name)
			fmt.Printf("🗑️ Skill removed: %s\n", name)
			return
		}
	}
}

func (h *HotLoader) FindMatching(query string) []Skill {
	h.mu.RLock()
	defer h.mu.RUnlock()

	var matches []Skill
	queryLower := strings.ToLower(query)

	for _, skill := range h.skills {
		// Always-скиллы всегда активны
		if skill.TriggerMode == "always" {
			matches = append(matches, *skill)
			continue
		}

		// Auto-скиллы — проверяем keywords
		if skill.TriggerMode == "auto" {
			for _, kw := range skill.Keywords {
				if strings.Contains(queryLower, strings.ToLower(kw)) {
					matches = append(matches, *skill)
					break
				}
			}
		}
	}

	return matches
}

func (h *HotLoader) Activate(name string) (*Skill, error) {
	h.mu.Lock()
	defer h.mu.Unlock()

	skill, ok := h.skills[name]
	if !ok {
		return nil, fmt.Errorf("skill not found: %s", name)
	}

	h.active[name] = true
	return skill, nil
}

func (h *HotLoader) GetAll() []Skill {
	h.mu.RLock()
	defer h.mu.RUnlock()

	var result []Skill
	for _, s := range h.skills {
		result = append(result, *s)
	}
	return result
}

func (h *HotLoader) GetActive() []Skill {
	h.mu.RLock()
	defer h.mu.RUnlock()

	var result []Skill
	for name := range h.active {
		if s, ok := h.skills[name]; ok {
			result = append(result, *s)
		}
	}
	return result
}

func (h *HotLoader) Reload() error {
	return h.scanDir()
}

func (h *HotLoader) Close() error {
	close(h.stopWatch)
	return h.watcher.Close()
}

// BuildPrompt строит промпт из активных скиллов
func (h *HotLoader) BuildPrompt(query string) string {
	h.mu.RLock()
	defer h.mu.RUnlock()

	var parts []string
	parts = append(parts, "You are XLI-Go Bot with the following skills activated:\n")

	for name := range h.active {
		if skill, ok := h.skills[name]; ok {
			parts = append(parts, fmt.Sprintf("=== %s ===\n%s\n", skill.Name, skill.Content))
		}
	}

	// Добавляем matching auto-скиллы
	for _, skill := range h.skills {
		if skill.TriggerMode != "auto" {
			continue
		}
		queryLower := strings.ToLower(query)
		for _, kw := range skill.Keywords {
			if strings.Contains(queryLower, strings.ToLower(kw)) {
				parts = append(parts, fmt.Sprintf("=== %s (auto-triggered) ===\n%s\n", skill.Name, skill.Content))
				break
			}
		}
	}

	return strings.Join(parts, "\n")
}
