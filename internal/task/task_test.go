package task

// Automated tests for small text helpers in this package.
// Run with "go test ./..." from the project root.

import "testing"

// TestRepeatPhrase checks the plain-English wording shown in plan summaries.
func TestRepeatPhrase(t *testing.T) {
	cases := map[int]string{
		1: "every week",
		2: "every 2 weeks",
		3: "every 3 weeks",
		4: "every 4 weeks",
	}
	for weeks, want := range cases {
		if got := RepeatPhrase(weeks); got != want {
			t.Errorf("RepeatPhrase(%d) = %q, want %q", weeks, got, want)
		}
	}
}
