package memory

import (
	"database/sql"
	"time"

	_ "modernc.org/sqlite"
)

type Store interface {
	SaveMessage(chatID int64, role, content string) error
	LoadHistory(chatID int64, limit int) ([]Message, error)
	CompactHistory(chatID int64) error
	ClearHistory(chatID int64) error
	SaveToolMemory(chatID int64, toolName, output string) error
	LoadToolMemory(chatID int64) []string
}

type Message struct {
	ID        int64
	ChatID    int64
	Role      string
	Content   string
	Timestamp time.Time
}

type SQLiteStore struct {
	db *sql.DB
}

func NewSQLiteStore(dbPath string) (*SQLiteStore, error) {
	db, err := sql.Open("sqlite", dbPath)
	if err != nil {
		return nil, err
	}

	store := &SQLiteStore{db: db}
	if err := store.initSchema(); err != nil {
		return nil, err
	}
	return store, nil
}

func (s *SQLiteStore) initSchema() error {
	schema := `
	CREATE TABLE IF NOT EXISTS messages (
		id INTEGER PRIMARY KEY AUTOINCREMENT,
		chat_id INTEGER NOT NULL,
		role TEXT NOT NULL,
		content TEXT NOT NULL,
		timestamp DATETIME DEFAULT CURRENT_TIMESTAMP
	);
	CREATE INDEX IF NOT EXISTS idx_messages_chat ON messages(chat_id);

	CREATE TABLE IF NOT EXISTS agent_logs (
		id INTEGER PRIMARY KEY AUTOINCREMENT,
		chat_id INTEGER,
		tool_name TEXT,
		result TEXT,
		timestamp DATETIME DEFAULT CURRENT_TIMESTAMP
	);

	CREATE TABLE IF NOT EXISTS token_usage (
		id INTEGER PRIMARY KEY AUTOINCREMENT,
		chat_id INTEGER,
		provider TEXT,
		model TEXT,
		input_tokens INTEGER,
		output_tokens INTEGER,
		total_tokens INTEGER,
		timestamp DATETIME DEFAULT CURRENT_TIMESTAMP
	);
	`
	_, err := s.db.Exec(schema)
	return err
}

func (s *SQLiteStore) SaveMessage(chatID int64, role, content string) error {
	_, err := s.db.Exec(
		"INSERT INTO messages (chat_id, role, content) VALUES (?, ?, ?)",
		chatID, role, content,
	)
	return err
}

func (s *SQLiteStore) LoadHistory(chatID int64, limit int) ([]Message, error) {
	if limit <= 0 {
		limit = 50
	}
	rows, err := s.db.Query(
		"SELECT id, chat_id, role, content, timestamp FROM messages WHERE chat_id = ? ORDER BY timestamp DESC LIMIT ?",
		chatID, limit,
	)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var messages []Message
	for rows.Next() {
		var msg Message
		err := rows.Scan(&msg.ID, &msg.ChatID, &msg.Role, &msg.Content, &msg.Timestamp)
		if err != nil {
			return nil, err
		}
		messages = append(messages, msg)
	}

	// Reverse to chronological order
	for i, j := 0, len(messages)-1; i < j; i, j = i+1, j-1 {
		messages[i], messages[j] = messages[j], messages[i]
	}
	return messages, nil
}

func (s *SQLiteStore) CompactHistory(chatID int64) error {
	_, err := s.db.Exec(`
		DELETE FROM messages WHERE chat_id = ? AND id NOT IN (
			SELECT id FROM messages WHERE chat_id = ? ORDER BY timestamp DESC LIMIT 20
		)
	`, chatID, chatID)
	return err
}

func (s *SQLiteStore) ClearHistory(chatID int64) error {
	_, err := s.db.Exec("DELETE FROM messages WHERE chat_id = ?", chatID)
	return err
}

func (s *SQLiteStore) SaveToolMemory(chatID int64, toolName, output string) error {
	_, err := s.db.Exec(
		"INSERT INTO agent_logs (chat_id, tool_name, result) VALUES (?, ?, ?)",
		chatID, toolName, output,
	)
	return err
}

func (s *SQLiteStore) LoadToolMemory(chatID int64) []string {
	rows, err := s.db.Query(
		"SELECT result FROM agent_logs WHERE chat_id = ? ORDER BY timestamp DESC LIMIT 20",
		chatID,
	)
	if err != nil {
		return nil
	}
	defer rows.Close()

	var results []string
	for rows.Next() {
		var result string
		if err := rows.Scan(&result); err == nil {
			results = append(results, result)
		}
	}
	return results
}

func (s *SQLiteStore) Close() error {
	return s.db.Close()
}
