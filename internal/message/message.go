package message

import (
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"time"
)

// Role identifies who authored a message.
type Role string

const (
	User      Role = "user"
	Assistant Role = "assistant"
	System    Role = "system"
	Tool      Role = "tool"
)

// ToolCall represents a tool invocation requested by the LLM.
type ToolCall struct {
	ID    string          `json:"id"`
	Name  string          `json:"name"`
	Input json.RawMessage `json:"input"`
}

// ToolResult represents the output of a tool execution.
type ToolResult struct {
	ToolCallID string `json:"tool_call_id"`
	Name       string `json:"name"`
	Output     string `json:"output"`
	IsError    bool   `json:"is_error"`
}

// Message represents a single message in a conversation.
type Message struct {
	ID          string       `json:"id"`
	SessionID   string       `json:"session_id"`
	Role        Role         `json:"role"`
	Content     string       `json:"content"`
	ToolCalls   []ToolCall   `json:"tool_calls,omitempty"`
	ToolResults []ToolResult `json:"tool_results,omitempty"`
	CreatedAt   time.Time    `json:"created_at"`
}

// IsToolCall returns true if this message contains tool call requests.
func (m Message) IsToolCall() bool {
	return len(m.ToolCalls) > 0
}

// IsToolResult returns true if this message contains tool results.
func (m Message) IsToolResult() bool {
	return len(m.ToolResults) > 0
}

// NewUserMessage creates a new user message.
func NewUserMessage(sessionID, content string) Message {
	return Message{
		ID:        generateID(),
		SessionID: sessionID,
		Role:      User,
		Content:   content,
		CreatedAt: time.Now(),
	}
}

// NewAssistantMessage creates a new assistant message.
func NewAssistantMessage(sessionID, content string, toolCalls []ToolCall) Message {
	return Message{
		ID:        generateID(),
		SessionID: sessionID,
		Role:      Assistant,
		Content:   content,
		ToolCalls: toolCalls,
		CreatedAt: time.Now(),
	}
}

// NewToolResultMessage creates a new tool result message.
func NewToolResultMessage(sessionID string, results []ToolResult) Message {
	return Message{
		ID:          generateID(),
		SessionID:   sessionID,
		Role:        Tool,
		ToolResults: results,
		CreatedAt:   time.Now(),
	}
}

// NewSystemMessage creates a new system message.
func NewSystemMessage(sessionID, content string) Message {
	return Message{
		ID:        generateID(),
		SessionID: sessionID,
		Role:      System,
		Content:   content,
		CreatedAt: time.Now(),
	}
}

// generateID produces a unique ID using timestamp + random bytes.
func generateID() string {
	b := make([]byte, 8)
	_, _ = rand.Read(b)
	return fmt.Sprintf("msg_%s_%s",
		time.Now().Format("20060102150405"),
		hex.EncodeToString(b),
	)
}
