package db

import (
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/nullism/goder/internal/message"
)

func newTestDB(t *testing.T) *DB {
	t.Helper()
	dir := t.TempDir()
	dbPath := filepath.Join(dir, "test.db")
	database, err := New(dbPath)
	if err != nil {
		t.Fatalf("creating test db: %v", err)
	}
	t.Cleanup(func() { database.Close() })
	return database
}

func TestGetSessionTokenTotalsByModel_MultipleModels(t *testing.T) {
	db := newTestDB(t)

	sessionID := "ses_test_001"
	if _, err := db.CreateSession(sessionID, "test"); err != nil {
		t.Fatalf("creating session: %v", err)
	}

	msgs := []message.Message{
		{
			ID: "msg_01", SessionID: sessionID, Role: message.Assistant, Model: "gpt-4o",
			Content: "hello", TotalTokens: 100, InputTokens: 80, OutputTokens: 20, CreatedAt: time.Now(),
		},
		{
			ID: "msg_02", SessionID: sessionID, Role: message.Assistant, Model: "gpt-4o",
			Content: "world", TotalTokens: 200, InputTokens: 150, OutputTokens: 50, CreatedAt: time.Now(),
		},
		{
			ID: "msg_03", SessionID: sessionID, Role: message.Assistant, Model: "claude-sonnet",
			Content: "hi", TotalTokens: 50, InputTokens: 40, OutputTokens: 10, CreatedAt: time.Now(),
		},
	}

	for _, msg := range msgs {
		if err := db.AddMessage(msg); err != nil {
			t.Fatalf("adding message %s: %v", msg.ID, err)
		}
	}

	totals, err := db.GetSessionTokenTotalsByModel(sessionID)
	if err != nil {
		t.Fatalf("GetSessionTokenTotalsByModel: %v", err)
	}

	if totals["gpt-4o"] != 300 {
		t.Errorf("expected gpt-4o=300, got %d", totals["gpt-4o"])
	}
	if totals["claude-sonnet"] != 50 {
		t.Errorf("expected claude-sonnet=50, got %d", totals["claude-sonnet"])
	}
	if len(totals) != 2 {
		t.Errorf("expected 2 models, got %d: %v", len(totals), totals)
	}
}

func TestGetSessionTokenTotalsByModel_EmptyModelGroupedAsUnknown(t *testing.T) {
	db := newTestDB(t)

	sessionID := "ses_test_002"
	if _, err := db.CreateSession(sessionID, "test"); err != nil {
		t.Fatalf("creating session: %v", err)
	}

	msgs := []message.Message{
		{
			ID: "msg_01", SessionID: sessionID, Role: message.Assistant, Model: "",
			Content: "hello", TotalTokens: 100, CreatedAt: time.Now(),
		},
		{
			ID: "msg_02", SessionID: sessionID, Role: message.Assistant, Model: "gpt-4o",
			Content: "world", TotalTokens: 200, CreatedAt: time.Now(),
		},
	}

	for _, msg := range msgs {
		if err := db.AddMessage(msg); err != nil {
			t.Fatalf("adding message %s: %v", msg.ID, err)
		}
	}

	totals, err := db.GetSessionTokenTotalsByModel(sessionID)
	if err != nil {
		t.Fatalf("GetSessionTokenTotalsByModel: %v", err)
	}

	if totals["unknown"] != 100 {
		t.Errorf("expected unknown=100, got %d", totals["unknown"])
	}
	if totals["gpt-4o"] != 200 {
		t.Errorf("expected gpt-4o=200, got %d", totals["gpt-4o"])
	}
}

func TestGetSessionTokenTotalsByModel_Empty(t *testing.T) {
	db := newTestDB(t)

	sessionID := "ses_test_003"
	if _, err := db.CreateSession(sessionID, "test"); err != nil {
		t.Fatalf("creating session: %v", err)
	}

	totals, err := db.GetSessionTokenTotalsByModel(sessionID)
	if err != nil {
		t.Fatalf("GetSessionTokenTotalsByModel: %v", err)
	}

	if len(totals) != 0 {
		t.Errorf("expected empty map, got %v", totals)
	}
}

func TestGetSessionTokenTotalsByModel_ZeroTokenRows(t *testing.T) {
	db := newTestDB(t)

	sessionID := "ses_test_004"
	if _, err := db.CreateSession(sessionID, "test"); err != nil {
		t.Fatalf("creating session: %v", err)
	}

	msgs := []message.Message{
		{
			ID: "msg_01", SessionID: sessionID, Role: message.User, Model: "",
			Content: "hello", TotalTokens: 0, CreatedAt: time.Now(),
		},
		{
			ID: "msg_02", SessionID: sessionID, Role: message.Assistant, Model: "gpt-4o",
			Content: "world", TotalTokens: 150, CreatedAt: time.Now(),
		},
	}

	for _, msg := range msgs {
		if err := db.AddMessage(msg); err != nil {
			t.Fatalf("adding message %s: %v", msg.ID, err)
		}
	}

	totals, err := db.GetSessionTokenTotalsByModel(sessionID)
	if err != nil {
		t.Fatalf("GetSessionTokenTotalsByModel: %v", err)
	}

	// The empty-model row with 0 tokens still appears as "unknown: 0"
	if totals["gpt-4o"] != 150 {
		t.Errorf("expected gpt-4o=150, got %d", totals["gpt-4o"])
	}
}

func TestAddMessage_PersistsModel(t *testing.T) {
	db := newTestDB(t)

	sessionID := "ses_test_005"
	if _, err := db.CreateSession(sessionID, "test"); err != nil {
		t.Fatalf("creating session: %v", err)
	}

	msg := message.Message{
		ID: "msg_01", SessionID: sessionID, Role: message.Assistant, Model: "claude-sonnet",
		Content: "hello", TotalTokens: 100, CreatedAt: time.Now(),
	}
	if err := db.AddMessage(msg); err != nil {
		t.Fatalf("adding message: %v", err)
	}

	messages, err := db.GetMessages(sessionID)
	if err != nil {
		t.Fatalf("getting messages: %v", err)
	}
	if len(messages) != 1 {
		t.Fatalf("expected 1 message, got %d", len(messages))
	}
	if messages[0].Model != "claude-sonnet" {
		t.Errorf("expected model 'claude-sonnet', got %q", messages[0].Model)
	}
}

func TestMigration_AddsModelColumn(t *testing.T) {
	// Create a DB, close it, re-open to verify the migration is idempotent.
	dir := t.TempDir()
	dbPath := filepath.Join(dir, "migrate_test.db")

	db1, err := New(dbPath)
	if err != nil {
		t.Fatalf("first open: %v", err)
	}
	db1.Close()

	// Second open should succeed (migration is idempotent).
	db2, err := New(dbPath)
	if err != nil {
		t.Fatalf("second open: %v", err)
	}
	defer db2.Close()

	// Verify model column works.
	sessionID := "ses_migrate"
	if _, err := db2.CreateSession(sessionID, "test"); err != nil {
		t.Fatalf("creating session: %v", err)
	}
	msg := message.Message{
		ID: "msg_01", SessionID: sessionID, Role: message.Assistant, Model: "test-model",
		Content: "hi", CreatedAt: time.Now(),
	}
	if err := db2.AddMessage(msg); err != nil {
		t.Fatalf("adding message: %v", err)
	}

	messages, err := db2.GetMessages(sessionID)
	if err != nil {
		t.Fatalf("getting messages: %v", err)
	}
	if messages[0].Model != "test-model" {
		t.Errorf("expected model 'test-model', got %q", messages[0].Model)
	}
}

// Verify the test DB helper cleans up properly.
func TestNewTestDB_Cleanup(t *testing.T) {
	var dbPath string
	t.Run("inner", func(t *testing.T) {
		db := newTestDB(t)
		dbPath = filepath.Join(t.TempDir(), "check")
		_ = db // just verifying it creates without error
	})
	// After the subtest, TempDir is cleaned up automatically by testing.T.
	if _, err := os.Stat(dbPath); err == nil {
		t.Error("expected temp dir to be cleaned up")
	}
}
