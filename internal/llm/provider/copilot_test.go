package provider

import (
	"context"
	"strings"
	"testing"
)

func runCopilotChatStream(t *testing.T, lines []string) []StreamEvent {
	t.Helper()

	p := &CopilotProvider{}
	eventsCh := make(chan StreamEvent, 64)

	go func() {
		defer close(eventsCh)
		p.processStream(context.Background(), strings.NewReader(strings.Join(lines, "\n")), eventsCh)
	}()

	var events []StreamEvent
	for ev := range eventsCh {
		events = append(events, ev)
	}
	return events
}

func runCopilotResponsesStream(t *testing.T, lines []string) []StreamEvent {
	t.Helper()

	p := &CopilotProvider{}
	eventsCh := make(chan StreamEvent, 64)

	go func() {
		defer close(eventsCh)
		p.processResponsesStream(context.Background(), strings.NewReader(strings.Join(lines, "\n")), eventsCh)
	}()

	var events []StreamEvent
	for ev := range eventsCh {
		events = append(events, ev)
	}
	return events
}

func TestCopilotProcessStream_UsageWithChoicesDoesNotTerminateEarly(t *testing.T) {
	events := runCopilotChatStream(t, []string{
		`data: {"id":"chunk-1","choices":[{"index":0,"delta":{"content":null,"role":"assistant","reasoning_text":"thinking"},"finish_reason":null}],"usage":{"prompt_tokens":0,"completion_tokens":0,"total_tokens":0}}`,
		`data: {"id":"chunk-2","choices":[{"index":0,"delta":{"content":null,"role":"assistant","tool_calls":[{"index":0,"id":"call_1","type":"function","function":{"name":"read_file","arguments":"{\"filePath\":\"/README.md\"}"}}]},"finish_reason":"tool_calls"}],"usage":{"prompt_tokens":34,"completion_tokens":12,"total_tokens":46}}`,
		`data: [DONE]`,
	})

	var (
		gotStart bool
		gotDelta bool
		gotEnd   bool
		gotDone  bool
	)

	for _, ev := range events {
		switch ev.Type {
		case EventToolCallStart:
			if ev.ToolCallID == "call_1" && ev.ToolCallName == "read_file" {
				gotStart = true
			}
		case EventToolCallDelta:
			if ev.ToolCallID == "call_1" && strings.Contains(ev.ToolCallInput, `"filePath":"/README.md"`) {
				gotDelta = true
			}
		case EventToolCallEnd:
			if ev.ToolCallID == "call_1" && ev.ToolCallName == "read_file" && strings.Contains(ev.ToolCallInput, `"filePath":"/README.md"`) {
				gotEnd = true
			}
		case EventDone:
			gotDone = true
			if ev.Usage.InputTokens != 34 || ev.Usage.OutputTokens != 12 || ev.Usage.TotalTokens != 46 {
				t.Fatalf("unexpected usage in done event: %+v", ev.Usage)
			}
		case EventError:
			t.Fatalf("unexpected stream error: %v", ev.Error)
		}
	}

	if !gotStart || !gotDelta || !gotEnd {
		t.Fatalf("expected complete tool-call lifecycle, got start=%v delta=%v end=%v", gotStart, gotDelta, gotEnd)
	}
	if !gotDone {
		t.Fatal("expected done event")
	}
}

func TestCopilotProcessStream_NullContentDoesNotBlockLaterText(t *testing.T) {
	events := runCopilotChatStream(t, []string{
		`data: {"id":"chunk-1","choices":[{"index":0,"delta":{"content":null,"role":"assistant"},"finish_reason":null}],"usage":{"prompt_tokens":0,"completion_tokens":0,"total_tokens":0}}`,
		`data: {"id":"chunk-2","choices":[{"index":0,"delta":{"content":"Hello","role":"assistant"},"finish_reason":"stop"}],"usage":{"prompt_tokens":11,"completion_tokens":3,"total_tokens":14}}`,
		`data: [DONE]`,
	})

	var (
		gotText string
		gotDone bool
	)

	for _, ev := range events {
		switch ev.Type {
		case EventTextDelta:
			gotText += ev.Text
		case EventDone:
			gotDone = true
			if ev.Usage.InputTokens != 11 || ev.Usage.OutputTokens != 3 || ev.Usage.TotalTokens != 14 {
				t.Fatalf("unexpected usage in done event: %+v", ev.Usage)
			}
		case EventError:
			t.Fatalf("unexpected stream error: %v", ev.Error)
		}
	}

	if gotText != "Hello" {
		t.Fatalf("expected streamed text 'Hello', got %q", gotText)
	}
	if !gotDone {
		t.Fatal("expected done event")
	}
}

func TestCopilotProcessStream_MissingToolCallIDUsesSyntheticID(t *testing.T) {
	events := runCopilotChatStream(t, []string{
		`data: {"id":"chunk-1","choices":[{"index":0,"delta":{"content":null,"role":"assistant","tool_calls":[{"index":0,"type":"function","function":{"name":"list_files","arguments":"{}"}}]},"finish_reason":"tool_calls"}],"usage":{"prompt_tokens":4,"completion_tokens":2,"total_tokens":6}}`,
		`data: [DONE]`,
	})

	var (
		gotSyntheticStart bool
		gotSyntheticEnd   bool
	)

	for _, ev := range events {
		switch ev.Type {
		case EventToolCallStart:
			if ev.ToolCallID == "call_0" && ev.ToolCallName == "list_files" {
				gotSyntheticStart = true
			}
		case EventToolCallEnd:
			if ev.ToolCallID == "call_0" && ev.ToolCallName == "list_files" {
				gotSyntheticEnd = true
			}
		case EventError:
			t.Fatalf("unexpected stream error: %v", ev.Error)
		}
	}

	if !gotSyntheticStart || !gotSyntheticEnd {
		t.Fatalf("expected synthetic tool call id lifecycle, got start=%v end=%v", gotSyntheticStart, gotSyntheticEnd)
	}
}

func TestCopilotProcessStream_StructuredContentAndDataPrefix(t *testing.T) {
	events := runCopilotChatStream(t, []string{
		`data:{"id":"chunk-1","choices":[{"index":0,"delta":{"content":[{"type":"output_text","text":"VERDICT: APPROVE"},{"type":"output_text","text":"\n\nLooks good."}],"role":"assistant"},"finish_reason":"stop"}],"usage":{"prompt_tokens":7,"completion_tokens":5,"total_tokens":12}}`,
		`data:[DONE]`,
	})

	var (
		gotText string
		gotDone bool
	)

	for _, ev := range events {
		switch ev.Type {
		case EventTextDelta:
			gotText += ev.Text
		case EventDone:
			gotDone = true
			if ev.Usage.InputTokens != 7 || ev.Usage.OutputTokens != 5 || ev.Usage.TotalTokens != 12 {
				t.Fatalf("unexpected usage in done event: %+v", ev.Usage)
			}
		case EventError:
			t.Fatalf("unexpected stream error: %v", ev.Error)
		}
	}

	if gotText != "VERDICT: APPROVE\n\nLooks good." {
		t.Fatalf("expected structured content text, got %q", gotText)
	}
	if !gotDone {
		t.Fatal("expected done event")
	}
}

func TestCopilotProcessResponsesStream_DataPrefixWithoutSpace(t *testing.T) {
	events := runCopilotResponsesStream(t, []string{
		`data:{"type":"response.output_text.delta","delta":"hello"}`,
		`data:{"type":"response.completed","response":{"usage":{"input_tokens":3,"output_tokens":2}}}`,
	})

	var (
		gotText string
		gotDone bool
	)

	for _, ev := range events {
		switch ev.Type {
		case EventTextDelta:
			gotText += ev.Text
		case EventDone:
			gotDone = true
			if ev.Usage.InputTokens != 3 || ev.Usage.OutputTokens != 2 || ev.Usage.TotalTokens != 5 {
				t.Fatalf("unexpected usage in done event: %+v", ev.Usage)
			}
		case EventError:
			t.Fatalf("unexpected stream error: %v", ev.Error)
		}
	}

	if gotText != "hello" {
		t.Fatalf("expected streamed text 'hello', got %q", gotText)
	}
	if !gotDone {
		t.Fatal("expected done event")
	}
}
