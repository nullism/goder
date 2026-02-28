package db

import (
	"database/sql"
	"encoding/json"
	"fmt"
	"strings"
	"time"

	"github.com/webgovernor/goder/internal/message"

	_ "github.com/ncruces/go-sqlite3/driver"
	_ "github.com/ncruces/go-sqlite3/embed"
)

// DB wraps a SQLite database connection.
type DB struct {
	conn *sql.DB
}

// Session represents a conversation session.
type Session struct {
	ID        string
	Title     string
	Summary   string
	CreatedAt time.Time
	UpdatedAt time.Time
}

// New opens (or creates) a SQLite database at the given path and runs migrations.
func New(dbPath string) (*DB, error) {
	conn, err := sql.Open("sqlite3", dbPath)
	if err != nil {
		return nil, fmt.Errorf("opening database: %w", err)
	}

	// Enable WAL mode for better concurrent performance.
	if _, err := conn.Exec("PRAGMA journal_mode=WAL"); err != nil {
		conn.Close()
		return nil, fmt.Errorf("setting WAL mode: %w", err)
	}

	db := &DB{conn: conn}
	if err := db.migrate(); err != nil {
		conn.Close()
		return nil, fmt.Errorf("running migrations: %w", err)
	}

	return db, nil
}

// Close closes the database connection.
func (db *DB) Close() error {
	return db.conn.Close()
}

// migrate creates the schema tables if they don't exist.
func (db *DB) migrate() error {
	schema := `
	CREATE TABLE IF NOT EXISTS sessions (
		id         TEXT PRIMARY KEY,
		title      TEXT NOT NULL DEFAULT '',
		summary    TEXT NOT NULL DEFAULT '',
		created_at DATETIME NOT NULL DEFAULT (datetime('now')),
		updated_at DATETIME NOT NULL DEFAULT (datetime('now'))
	);

	CREATE TABLE IF NOT EXISTS messages (
		id           TEXT PRIMARY KEY,
		session_id   TEXT NOT NULL,
		role         TEXT NOT NULL,
		content      TEXT NOT NULL DEFAULT '',
		tool_calls   TEXT NOT NULL DEFAULT '[]',
		tool_results TEXT NOT NULL DEFAULT '[]',
		input_tokens INTEGER NOT NULL DEFAULT 0,
		output_tokens INTEGER NOT NULL DEFAULT 0,
		total_tokens INTEGER NOT NULL DEFAULT 0,
		created_at   DATETIME NOT NULL DEFAULT (datetime('now')),
		FOREIGN KEY (session_id) REFERENCES sessions(id) ON DELETE CASCADE
	);

	CREATE INDEX IF NOT EXISTS idx_messages_session_id ON messages(session_id);
	`
	_, err := db.conn.Exec(schema)
	if err != nil {
		return err
	}

	// Backward-compatible migrations for existing databases.
	if _, err := db.conn.Exec("ALTER TABLE messages ADD COLUMN input_tokens INTEGER NOT NULL DEFAULT 0"); err != nil && !strings.Contains(err.Error(), "duplicate column name") {
		return err
	}
	if _, err := db.conn.Exec("ALTER TABLE messages ADD COLUMN output_tokens INTEGER NOT NULL DEFAULT 0"); err != nil && !strings.Contains(err.Error(), "duplicate column name") {
		return err
	}
	if _, err := db.conn.Exec("ALTER TABLE messages ADD COLUMN total_tokens INTEGER NOT NULL DEFAULT 0"); err != nil && !strings.Contains(err.Error(), "duplicate column name") {
		return err
	}

	return nil
}

// --- Session operations ---

// CreateSession creates a new session and returns it.
func (db *DB) CreateSession(id, title string) (*Session, error) {
	now := time.Now()
	_, err := db.conn.Exec(
		"INSERT INTO sessions (id, title, created_at, updated_at) VALUES (?, ?, ?, ?)",
		id, title, now, now,
	)
	if err != nil {
		return nil, err
	}
	return &Session{
		ID:        id,
		Title:     title,
		CreatedAt: now,
		UpdatedAt: now,
	}, nil
}

// GetSession retrieves a session by ID.
func (db *DB) GetSession(id string) (*Session, error) {
	s := &Session{}
	err := db.conn.QueryRow(
		"SELECT id, title, summary, created_at, updated_at FROM sessions WHERE id = ?", id,
	).Scan(&s.ID, &s.Title, &s.Summary, &s.CreatedAt, &s.UpdatedAt)
	if err != nil {
		return nil, err
	}
	return s, nil
}

// ListSessions returns all sessions ordered by most recent first.
func (db *DB) ListSessions() ([]*Session, error) {
	rows, err := db.conn.Query(
		"SELECT id, title, summary, created_at, updated_at FROM sessions ORDER BY updated_at DESC",
	)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var sessions []*Session
	for rows.Next() {
		s := &Session{}
		if err := rows.Scan(&s.ID, &s.Title, &s.Summary, &s.CreatedAt, &s.UpdatedAt); err != nil {
			return nil, err
		}
		sessions = append(sessions, s)
	}
	return sessions, rows.Err()
}

// UpdateSessionTitle updates a session's title.
func (db *DB) UpdateSessionTitle(id, title string) error {
	_, err := db.conn.Exec(
		"UPDATE sessions SET title = ?, updated_at = datetime('now') WHERE id = ?",
		title, id,
	)
	return err
}

// DeleteSession deletes a session and its messages.
func (db *DB) DeleteSession(id string) error {
	tx, err := db.conn.Begin()
	if err != nil {
		return err
	}
	defer tx.Rollback()

	if _, err := tx.Exec("DELETE FROM messages WHERE session_id = ?", id); err != nil {
		return err
	}
	if _, err := tx.Exec("DELETE FROM sessions WHERE id = ?", id); err != nil {
		return err
	}
	return tx.Commit()
}

// --- Message operations ---

// AddMessage persists a message to the database.
func (db *DB) AddMessage(msg message.Message) error {
	toolCallsJSON, err := json.Marshal(msg.ToolCalls)
	if err != nil {
		return fmt.Errorf("marshaling tool calls: %w", err)
	}
	toolResultsJSON, err := json.Marshal(msg.ToolResults)
	if err != nil {
		return fmt.Errorf("marshaling tool results: %w", err)
	}

	_, err = db.conn.Exec(
		`INSERT INTO messages (id, session_id, role, content, tool_calls, tool_results, input_tokens, output_tokens, total_tokens, created_at)
		 VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		msg.ID, msg.SessionID, string(msg.Role), msg.Content,
		string(toolCallsJSON), string(toolResultsJSON), msg.InputTokens, msg.OutputTokens, msg.TotalTokens, msg.CreatedAt,
	)
	if err != nil {
		return fmt.Errorf("inserting message: %w", err)
	}

	// Touch the session's updated_at
	_, _ = db.conn.Exec("UPDATE sessions SET updated_at = datetime('now') WHERE id = ?", msg.SessionID)

	return nil
}

// GetMessages returns all messages for a session in chronological order.
func (db *DB) GetMessages(sessionID string) ([]message.Message, error) {
	rows, err := db.conn.Query(
		`SELECT id, session_id, role, content, tool_calls, tool_results, input_tokens, output_tokens, total_tokens, created_at
		 FROM messages WHERE session_id = ? ORDER BY created_at ASC`,
		sessionID,
	)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var messages []message.Message
	for rows.Next() {
		var msg message.Message
		var role string
		var toolCallsJSON, toolResultsJSON string

		if err := rows.Scan(
			&msg.ID, &msg.SessionID, &role, &msg.Content,
			&toolCallsJSON, &toolResultsJSON, &msg.InputTokens, &msg.OutputTokens, &msg.TotalTokens, &msg.CreatedAt,
		); err != nil {
			return nil, err
		}

		msg.Role = message.Role(role)

		if err := json.Unmarshal([]byte(toolCallsJSON), &msg.ToolCalls); err != nil {
			return nil, fmt.Errorf("unmarshaling tool calls: %w", err)
		}
		if err := json.Unmarshal([]byte(toolResultsJSON), &msg.ToolResults); err != nil {
			return nil, fmt.Errorf("unmarshaling tool results: %w", err)
		}

		messages = append(messages, msg)
	}

	return messages, rows.Err()
}

// GetSessionTokenTotal returns the total tokens used in a session.
func (db *DB) GetSessionTokenTotal(sessionID string) (int, error) {
	var total int
	err := db.conn.QueryRow(
		"SELECT COALESCE(SUM(total_tokens), 0) FROM messages WHERE session_id = ?", sessionID,
	).Scan(&total)
	return total, err
}
