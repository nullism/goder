package tui

import "testing"

func TestShouldUseReviewedPlanning(t *testing.T) {
	tests := []struct {
		name   string
		prompt string
		want   bool
	}{
		{
			name:   "simple informational question bypasses review",
			prompt: "What does planner.Run do?",
			want:   false,
		},
		{
			name:   "explain request bypasses review",
			prompt: "Explain how sanitizeHistory works",
			want:   false,
		},
		{
			name:   "tiny typo fix bypasses review",
			prompt: "Fix a typo in README",
			want:   false,
		},
		{
			name:   "few-lines update bypasses review",
			prompt: "Update a few lines in the docs wording",
			want:   false,
		},
		{
			name:   "question with implementation intent keeps review",
			prompt: "How can we add OAuth login to this app?",
			want:   true,
		},
		{
			name:   "feature request keeps review",
			prompt: "Implement a new API endpoint for billing events",
			want:   true,
		},
		{
			name:   "refactor request keeps review",
			prompt: "Refactor auth flow and add migration steps",
			want:   true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := shouldUseReviewedPlanning(tt.prompt)
			if got != tt.want {
				t.Fatalf("shouldUseReviewedPlanning(%q) = %v, want %v", tt.prompt, got, tt.want)
			}
		})
	}
}
