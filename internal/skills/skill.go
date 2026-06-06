package skills

import "time"

type Skill struct {
	Name        string
	Description string
	Keywords    []string
	Content     string
	TriggerMode string // auto, always, off
	Source      string
	Modified    time.Time
}

type Registry interface {
	LoadFromDir(dir string) error
	FindMatching(query string) []Skill
	Activate(name string) (*Skill, error)
	GetAll() []Skill
	GetActive() []Skill
	BuildPrompt(query string) string
	Reload() error
}
