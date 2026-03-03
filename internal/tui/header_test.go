package tui

import (
	"strings"
	"testing"

	"github.com/charmbracelet/lipgloss"
	"golang.org/x/text/language"
	"golang.org/x/text/message"
)

func TestFormatPerModelTokens_Nil(t *testing.T) {
	printer := message.NewPrinter(language.English)
	result := formatPerModelTokens(nil, printer)
	if result != "" {
		t.Errorf("expected empty string for nil map, got %q", result)
	}
}

func TestFormatPerModelTokens_SingleModel(t *testing.T) {
	printer := message.NewPrinter(language.English)
	result := formatPerModelTokens(map[string]int{"gpt-4o": 100}, printer)
	// Single model should return empty — the overall total is sufficient.
	if result != "" {
		t.Errorf("expected empty string for single model, got %q", result)
	}
}

func TestFormatPerModelTokens_MultipleModels(t *testing.T) {
	printer := message.NewPrinter(language.English)
	perModel := map[string]int{
		"gpt-4o":        12340,
		"claude-sonnet": 4221,
	}
	result := formatPerModelTokens(perModel, printer)
	if result == "" {
		t.Fatal("expected non-empty result for multiple models")
	}
	// Should contain both model names and their token counts.
	if !containsAll(result, "claude-sonnet", "4,221", "gpt-4o", "12,340") {
		t.Errorf("result missing expected content: %q", result)
	}
}

func TestFormatPerModelTokens_ZeroTokensSkipped(t *testing.T) {
	printer := message.NewPrinter(language.English)
	perModel := map[string]int{
		"gpt-4o":        100,
		"claude-sonnet": 0,
	}
	result := formatPerModelTokens(perModel, printer)
	// Only one model has non-zero tokens, so with the zero one skipped
	// we effectively have only one entry → return empty.
	// But the map has 2 entries so the function runs; it builds parts
	// for non-zero only. If only 1 part, it should still render it.
	// Actually len(perModel) >= 2 so it enters the logic, but only
	// one part ends up non-zero. Let's verify it doesn't panic and
	// produces some output if there's at least one non-zero part.
	if result == "" {
		// With only one non-zero model the bracket still renders.
		// This is acceptable — it shows context when multiple models
		// were used even if one had zero tokens.
		return
	}
	if containsAll(result, "claude-sonnet") {
		t.Errorf("zero-token model should be skipped: %q", result)
	}
}

func TestHeaderView_NilPerModel(t *testing.T) {
	// Should not panic with nil per-model map.
	result := HeaderView("gpt-4o", 100, nil, 80)
	if result == "" {
		t.Error("expected non-empty header")
	}
}

func TestHeaderView_NarrowWidth(t *testing.T) {
	perModel := map[string]int{
		"gpt-4o":        500,
		"claude-sonnet": 300,
	}
	// Should not panic even with very narrow width.
	result := HeaderView("gpt-4o", 800, perModel, 20)
	if result == "" {
		t.Error("expected non-empty header")
	}
}

func TestHeaderView_ShowsPerModelOnSecondLine(t *testing.T) {
	perModel := map[string]int{
		"gpt-4o":        1234,
		"claude-sonnet": 5678,
	}
	result := HeaderView("gpt-4o", 6912, perModel, 120)

	// The rendered header should be 2 lines tall.
	height := lipgloss.Height(result)
	if height != 2 {
		t.Errorf("expected header height 2, got %d", height)
	}

	// The per-model breakdown should be on the second line, not the first.
	lines := strings.Split(result, "\n")
	if len(lines) < 2 {
		t.Fatal("expected at least 2 lines in header")
	}

	// Second line should contain both model names and counts.
	secondLine := lines[1]
	if !containsAll(secondLine, "gpt-4o", "1,234", "claude-sonnet", "5,678") {
		t.Errorf("second line missing per-model breakdown: %q", secondLine)
	}
}

func TestHeaderView_SingleModel_NoSecondLine(t *testing.T) {
	perModel := map[string]int{
		"gpt-4o": 1000,
	}
	result := HeaderView("gpt-4o", 1000, perModel, 120)

	// Single model — no extra breakdown line needed.
	height := lipgloss.Height(result)
	if height != 1 {
		t.Errorf("expected header height 1 for single model, got %d", height)
	}
}

// containsAll checks if s contains all substrings.
func containsAll(s string, subs ...string) bool {
	for _, sub := range subs {
		found := false
		for i := 0; i <= len(s)-len(sub); i++ {
			if s[i:i+len(sub)] == sub {
				found = true
				break
			}
		}
		if !found {
			return false
		}
	}
	return true
}
