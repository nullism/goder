package tui

import "strings"

var informationalOpeners = []string{
	"what ",
	"why ",
	"how ",
	"where ",
	"when ",
	"who ",
	"which ",
	"can you explain",
	"could you explain",
	"help me understand",
	"explain ",
	"describe ",
	"summarize ",
	"what does",
	"how does",
}

var implementationSignals = []string{
	"add ",
	"implement",
	"change ",
	"modify ",
	"update ",
	"create ",
	"fix ",
	"refactor",
	"rename ",
	"remove ",
	"delete ",
	"write ",
	"edit ",
	"patch ",
}

var simpleRequestSignals = []string{
	"typo",
	"spelling",
	"grammar",
	"wording",
	"punctuation",
	"whitespace",
	"comment",
	"one line",
	"single line",
	"few lines",
	"a few lines",
	"tiny",
	"small",
	"quick fix",
}

var complexRequestSignals = []string{
	"feature",
	"refactor",
	"architecture",
	"migration",
	"migrate",
	"database",
	"schema",
	"endpoint",
	"api",
	"auth",
	"oauth",
	"security",
	"permission",
	"performance",
	"optimize",
	"across the codebase",
	"across the project",
	"several files",
	"multi-file",
	"test suite",
}

// shouldUseReviewedPlanning decides whether a prompt should go through
// the plan-review loop or directly to the main execution agent.
func shouldUseReviewedPlanning(prompt string) bool {
	normalized := strings.ToLower(strings.TrimSpace(prompt))
	if normalized == "" {
		return true
	}

	if isLikelyInformationalQuestion(normalized) {
		return false
	}

	if containsAny(normalized, simpleRequestSignals) && !containsAny(normalized, complexRequestSignals) {
		return false
	}

	if containsAny(normalized, complexRequestSignals) {
		return true
	}

	return true
}

func isLikelyInformationalQuestion(prompt string) bool {
	if strings.Contains(prompt, "?") && !containsAny(prompt, implementationSignals) {
		return true
	}

	if containsAnyPrefix(prompt, informationalOpeners) && !containsAny(prompt, implementationSignals) {
		return true
	}

	return false
}

func containsAny(text string, terms []string) bool {
	for _, term := range terms {
		if strings.Contains(text, term) {
			return true
		}
	}
	return false
}

func containsAnyPrefix(text string, prefixes []string) bool {
	for _, prefix := range prefixes {
		if strings.HasPrefix(text, prefix) {
			return true
		}
	}
	return false
}
