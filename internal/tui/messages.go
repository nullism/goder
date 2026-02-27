package tui

import (
	"fmt"
	"strings"
	"time"

	"github.com/charmbracelet/lipgloss"
)

// Role identifies who authored a message.
type Role int

const (
	UserRole Role = iota
	AssistantRole
	SystemRole
)

// Message represents a single chat message.
type Message struct {
	Role      Role
	Content   string
	Timestamp time.Time
}

// MessageList holds the conversation history and viewport state.
type MessageList struct {
	messages []Message
	offset   int // scroll offset (index of first visible message)
}

// NewMessageList creates an empty message list.
func NewMessageList() MessageList {
	return MessageList{}
}

// Add appends a message and auto-scrolls to the bottom.
func (ml *MessageList) Add(role Role, content string) {
	ml.messages = append(ml.messages, Message{
		Role:      role,
		Content:   content,
		Timestamp: time.Now(),
	})
	// auto-scroll to bottom
	ml.offset = len(ml.messages)
}

// ScrollUp moves the viewport up.
func (ml *MessageList) ScrollUp(lines int) {
	ml.offset -= lines
	if ml.offset < 0 {
		ml.offset = 0
	}
}

// ScrollDown moves the viewport down.
func (ml *MessageList) ScrollDown(lines int) {
	ml.offset += lines
	if ml.offset > len(ml.messages) {
		ml.offset = len(ml.messages)
	}
}

// View renders the message list within the given dimensions.
func (ml *MessageList) View(width, height int) string {
	if len(ml.messages) == 0 {
		empty := dimStyle.Render("No messages yet. Type a prompt below to get started.")
		return lipgloss.Place(width, height, lipgloss.Center, lipgloss.Center, empty)
	}

	var rendered []string
	for _, msg := range ml.messages {
		rendered = append(rendered, renderMessage(msg, width))
	}

	// Simple bottom-anchored viewport: show as many messages as fit from the bottom.
	var lines []string
	for _, r := range rendered {
		lines = append(lines, r)
	}

	content := strings.Join(lines, "\n\n")

	// Truncate to fit height (simple approach: split to lines, take last N)
	allLines := strings.Split(content, "\n")
	if len(allLines) > height {
		allLines = allLines[len(allLines)-height:]
	}

	result := strings.Join(allLines, "\n")

	// Pad to fill height
	currentLines := strings.Count(result, "\n") + 1
	if currentLines < height {
		result += strings.Repeat("\n", height-currentLines)
	}

	return result
}

func renderMessage(msg Message, width int) string {
	var roleLabel string
	switch msg.Role {
	case UserRole:
		roleLabel = userMsgStyle.Render("> you")
	case AssistantRole:
		roleLabel = assistantMsgStyle.Render("> assistant")
	case SystemRole:
		roleLabel = dimStyle.Render("> system")
	}

	ts := timestampStyle.Render(msg.Timestamp.Format("15:04:05"))
	header := fmt.Sprintf("%s  %s", roleLabel, ts)

	contentWidth := width - 4
	if contentWidth < 20 {
		contentWidth = 20
	}
	body := msgContentStyle.Width(contentWidth).Render(msg.Content)

	return header + "\n" + body
}
