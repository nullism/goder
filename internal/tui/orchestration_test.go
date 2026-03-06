package tui

import "testing"

func TestParseOrchestratorDecision(t *testing.T) {
	input := "ACTION: CALL_PROGRAMMER\nMESSAGE: Ready to implement approved plan\nPLAN:\n1) Edit a.go\n2) Run tests"
	d := parseOrchestratorDecision(input)

	if d.Action != "CALL_PROGRAMMER" {
		t.Fatalf("Action = %q, want %q", d.Action, "CALL_PROGRAMMER")
	}
	if d.Message != "Ready to implement approved plan" {
		t.Fatalf("Message = %q", d.Message)
	}
	if d.Plan == "" {
		t.Fatal("expected non-empty plan")
	}
}

func TestParseOrchestratorDecisionFallbackToRespond(t *testing.T) {
	input := "Some plain assistant response"
	d := parseOrchestratorDecision(input)

	if d.Action != "RESPOND" {
		t.Fatalf("Action = %q, want %q", d.Action, "RESPOND")
	}
	if d.Message != input {
		t.Fatalf("Message = %q, want %q", d.Message, input)
	}
}

func TestIsPlanApprovalPrompt(t *testing.T) {
	if !isPlanApprovalPrompt("go ahead") {
		t.Fatal("expected go-ahead to be treated as approval")
	}
	if isPlanApprovalPrompt("please change step 2") {
		t.Fatal("expected change request to not be treated as approval")
	}
}
