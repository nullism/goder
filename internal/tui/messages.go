package tui

import (
	"fmt"
	"strings"
	"time"

	"github.com/charmbracelet/lipgloss"
	"github.com/webgovernor/goder/internal/message"
)

// DisplayMessage represents a message as displayed in the TUI.
// This is separate from the domain message.Message to support streaming
// and tool call display states.
type DisplayMessage struct {
	Role      message.Role
	Content   string
	Timestamp time.Time

	// Tool call display
	IsToolCall   bool
	ToolName     string
	ToolInput    string
	ToolOutput   string
	ToolIsError  bool
	IsToolResult bool

	// Streaming state
	IsStreaming bool
}

// MessageList holds the conversation display state.
type MessageList struct {
	messages  []DisplayMessage
	offset    int // scroll offset (lines from bottom)
	streaming int // index of the current streaming message, or -1
}

// NewMessageList creates an empty message list.
func NewMessageList() MessageList {
	return MessageList{streaming: -1}
}

// Count returns the number of messages.
func (ml *MessageList) Count() int {
	return len(ml.messages)
}

// Add appends a message with the given role and content.
func (ml *MessageList) Add(role message.Role, content string) {
	ml.messages = append(ml.messages, DisplayMessage{
		Role:      role,
		Content:   content,
		Timestamp: time.Now(),
	})
	ml.scrollToBottom()
}

// AddMessage appends a domain message.
func (ml *MessageList) AddMessage(msg message.Message) {
	ml.messages = append(ml.messages, DisplayMessage{
		Role:      msg.Role,
		Content:   msg.Content,
		Timestamp: msg.CreatedAt,
	})
	ml.scrollToBottom()
}

// LoadFromMessages replaces the message list with messages from the database.
func (ml *MessageList) LoadFromMessages(msgs []message.Message) {
	ml.messages = nil
	for _, msg := range msgs {
		dm := DisplayMessage{
			Role:      msg.Role,
			Content:   msg.Content,
			Timestamp: msg.CreatedAt,
		}
		// Render tool calls and results from persisted messages
		if msg.IsToolCall() {
			for _, tc := range msg.ToolCalls {
				ml.messages = append(ml.messages, DisplayMessage{
					Role:       message.Assistant,
					Content:    msg.Content,
					Timestamp:  msg.CreatedAt,
					IsToolCall: true,
					ToolName:   tc.Name,
					ToolInput:  string(tc.Input),
				})
			}
			if msg.Content != "" {
				ml.messages = append(ml.messages, dm)
			}
		} else if msg.IsToolResult() {
			for _, tr := range msg.ToolResults {
				ml.messages = append(ml.messages, DisplayMessage{
					Role:         message.Tool,
					Timestamp:    msg.CreatedAt,
					IsToolResult: true,
					ToolName:     tr.Name,
					ToolOutput:   tr.Output,
					ToolIsError:  tr.IsError,
				})
			}
		} else {
			ml.messages = append(ml.messages, dm)
		}
	}
	ml.scrollToBottom()
}

// UpdateStreaming updates the currently streaming message, or creates one.
func (ml *MessageList) UpdateStreaming(content string) {
	if ml.streaming >= 0 && ml.streaming < len(ml.messages) {
		ml.messages[ml.streaming].Content = content
	} else {
		ml.streaming = len(ml.messages)
		ml.messages = append(ml.messages, DisplayMessage{
			Role:        message.Assistant,
			Content:     content,
			Timestamp:   time.Now(),
			IsStreaming: true,
		})
	}
	ml.scrollToBottom()
}

// FinalizeStreaming marks the streaming message as complete.
func (ml *MessageList) FinalizeStreaming(finalContent string) {
	if ml.streaming >= 0 && ml.streaming < len(ml.messages) {
		ml.messages[ml.streaming].Content = finalContent
		ml.messages[ml.streaming].IsStreaming = false
	}
	ml.streaming = -1
}

// AddToolCall adds a tool call indicator message.
func (ml *MessageList) AddToolCall(toolName, input string) {
	ml.messages = append(ml.messages, DisplayMessage{
		Role:       message.Assistant,
		Timestamp:  time.Now(),
		IsToolCall: true,
		ToolName:   toolName,
		ToolInput:  input,
	})
	ml.scrollToBottom()
}

// UpdateLastToolCall updates the last tool call message with the final input.
func (ml *MessageList) UpdateLastToolCall(toolName, input string) {
	for i := len(ml.messages) - 1; i >= 0; i-- {
		if ml.messages[i].IsToolCall && ml.messages[i].ToolName == toolName {
			ml.messages[i].ToolInput = input
			break
		}
	}
}

// AddToolResult adds a tool result message.
func (ml *MessageList) AddToolResult(toolName, output string, isError bool) {
	ml.messages = append(ml.messages, DisplayMessage{
		Role:         message.Tool,
		Timestamp:    time.Now(),
		IsToolResult: true,
		ToolName:     toolName,
		ToolOutput:   output,
		ToolIsError:  isError,
	})
	ml.scrollToBottom()
}

func (ml *MessageList) scrollToBottom() {
	ml.offset = 0
}

// ScrollUp moves the viewport up.
func (ml *MessageList) ScrollUp(lines int) {
	if lines <= 0 {
		return
	}
	ml.offset += lines
}

// ScrollDown moves the viewport down.
func (ml *MessageList) ScrollDown(lines int) {
	if lines <= 0 {
		return
	}
	ml.offset -= lines
	if ml.offset < 0 {
		ml.offset = 0
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
		rendered = append(rendered, renderDisplayMessage(msg, width))
	}

	content := strings.Join(rendered, "\n\n")

	// Truncate to fit height (simple approach: split to lines, take last N)
	allLines := strings.Split(content, "\n")
	lineCount := len(allLines)
	if height < 1 {
		return ""
	}

	start := lineCount - height - ml.offset
	if start < 0 {
		start = 0
	}
	end := start + height
	if end > lineCount {
		end = lineCount
	}

	visible := allLines[start:end]
	result := strings.Join(visible, "\n")

	// Pad to fill height
	currentLines := strings.Count(result, "\n") + 1
	if currentLines < height {
		result += strings.Repeat("\n", height-currentLines)
	}

	return result
}

func renderDisplayMessage(msg DisplayMessage, width int) string {
	// Tool call message
	if msg.IsToolCall {
		label := toolCallStyle.Render(fmt.Sprintf("  tool: %s", msg.ToolName))
		input := msg.ToolInput
		if len(input) > 200 {
			input = input[:200] + "..."
		}
		if input != "" {
			inputRendered := dimStyle.Render(fmt.Sprintf("  %s", input))
			return label + "\n" + inputRendered
		}
		return label
	}

	// Tool result message
	if msg.IsToolResult {
		output := msg.ToolOutput
		if len(output) > 500 {
			output = output[:500] + "\n... (truncated)"
		}
		style := toolResultStyle
		if msg.ToolIsError {
			style = toolErrorStyle
		}
		label := style.Render(fmt.Sprintf("  result: %s", msg.ToolName))
		outputRendered := dimStyle.Render(fmt.Sprintf("  %s", output))
		return label + "\n" + outputRendered
	}

	// Regular message
	var roleLabel string
	switch msg.Role {
	case message.User:
		roleLabel = userMsgStyle.Render("> you")
	case message.Assistant:
		if msg.IsStreaming {
			roleLabel = assistantMsgStyle.Render("> assistant") + " " + streamingIndicator.Render("...")
		} else {
			roleLabel = assistantMsgStyle.Render("> assistant")
		}
	case message.System:
		roleLabel = dimStyle.Render("> system")
	default:
		roleLabel = dimStyle.Render("> " + string(msg.Role))
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
