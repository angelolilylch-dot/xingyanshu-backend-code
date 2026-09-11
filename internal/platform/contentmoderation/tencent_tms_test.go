package contentmoderation

import "testing"

func TestDecisionFromSuggestion(t *testing.T) {
	tests := map[string]string{"Pass": DecisionPass, "Block": DecisionBlock, "Review": DecisionReview, "unknown": DecisionReview}
	for input, want := range tests {
		if got := decisionFromSuggestion(input); got != want {
			t.Fatalf("decisionFromSuggestion(%q) = %q, want %q", input, got, want)
		}
	}
}
