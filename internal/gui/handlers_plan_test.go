package gui

// Automated tests for the GUI's consolidation matcher.
// Run with "go test ./..." from the project root.
//
// TRIPWIRE: this logic exists in TWO places — findConsolidations here and an
// intentional mirror copy in cmd_plan.go (the CLI). This file and
// cmd_plan_test.go run the SAME table of scenarios against each copy. If you
// change the matching rules in one place and forget the other, the twin test
// fails and points you at the forgotten copy.

import (
	"testing"

	"github.com/littleguygreens/greenies/internal/farm"
)

func TestFindConsolidations(t *testing.T) {
	existing := []farm.Cycle{
		{CycleID: "existA", CropName: "sunnies", Trays: 2, SowDate: "2026-07-20"},
		{CycleID: "existB", CropName: "sunnies", Trays: 4, SowDate: "2026-07-27"},
		{CycleID: "existC", CropName: "pea", Trays: 3, SowDate: "2026-07-20"},
	}

	t.Run("same crop and sow date matches", func(t *testing.T) {
		newCycles := []farm.Cycle{{CropName: "sunnies", SowDate: "2026-07-20"}}
		matches := findConsolidations(existing, newCycles, 3)
		if len(matches) != 1 {
			t.Fatalf("expected 1 match, got %d", len(matches))
		}
		m := matches[0]
		if m.ExistingCycleID != "existA" {
			t.Errorf("matched cycle = %s, want existA", m.ExistingCycleID)
		}
		if m.CombinedTrays != 5 { // 2 existing + 3 new
			t.Errorf("combined trays = %d, want 5", m.CombinedTrays)
		}
		if m.WeekIndex != 0 {
			t.Errorf("week index = %d, want 0", m.WeekIndex)
		}
		if m.SowDateDisplay != "Mon Jul 20" {
			t.Errorf("display date = %q, want %q", m.SowDateDisplay, "Mon Jul 20")
		}
	})

	t.Run("different sow date does not match", func(t *testing.T) {
		newCycles := []farm.Cycle{{CropName: "sunnies", SowDate: "2026-07-21"}}
		if matches := findConsolidations(existing, newCycles, 3); len(matches) != 0 {
			t.Fatalf("expected no matches, got %d", len(matches))
		}
	})

	t.Run("different crop does not match", func(t *testing.T) {
		newCycles := []farm.Cycle{{CropName: "daikon", SowDate: "2026-07-20"}}
		if matches := findConsolidations(existing, newCycles, 3); len(matches) != 0 {
			t.Fatalf("expected no matches, got %d", len(matches))
		}
	})

	t.Run("weekly repeats match independently", func(t *testing.T) {
		// Three planned weeks: only the middle one (index 1, sown 07-27)
		// collides with an existing cycle. The match must carry that week's
		// index so the merge step knows WHICH planned cycle to merge.
		newCycles := []farm.Cycle{
			{CropName: "sunnies", SowDate: "2026-08-10"}, // week 0 — no match
			{CropName: "sunnies", SowDate: "2026-07-27"}, // week 1 — matches existB
			{CropName: "sunnies", SowDate: "2026-08-24"}, // week 2 — no match
		}
		matches := findConsolidations(existing, newCycles, 3)
		if len(matches) != 1 {
			t.Fatalf("expected 1 match, got %d", len(matches))
		}
		if matches[0].WeekIndex != 1 || matches[0].ExistingCycleID != "existB" {
			t.Errorf("got week %d / cycle %s, want week 1 / existB",
				matches[0].WeekIndex, matches[0].ExistingCycleID)
		}
		if matches[0].CombinedTrays != 7 { // 4 existing + 3 new
			t.Errorf("combined trays = %d, want 7", matches[0].CombinedTrays)
		}
	})

	t.Run("only one existing cycle merges per new cycle", func(t *testing.T) {
		// Two existing cycles on the same crop + date (the grower chose
		// "add separately" once before). A new plan should merge with the
		// FIRST one only — one match, not two.
		doubled := append([]farm.Cycle{}, existing...)
		doubled = append(doubled, farm.Cycle{
			CycleID: "existA2", CropName: "sunnies", Trays: 1, SowDate: "2026-07-20",
		})
		newCycles := []farm.Cycle{{CropName: "sunnies", SowDate: "2026-07-20"}}
		matches := findConsolidations(doubled, newCycles, 3)
		if len(matches) != 1 || matches[0].ExistingCycleID != "existA" {
			t.Fatalf("expected a single match with the first existing cycle, got: %+v", matches)
		}
	})
}
