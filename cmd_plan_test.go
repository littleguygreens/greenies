package main

// Automated tests for the CLI's consolidation matcher and the shared
// parseDate helper. Run with "go test ./..." from the project root.
//
// TRIPWIRE: findConsolidations here is an intentional mirror of the one in
// internal/gui/handlers_plan.go. This file and handlers_plan_test.go run the
// SAME table of scenarios against each copy — change the matching rules in
// one place and forget the other, and the twin test fails to remind you.
// (The struct field names differ in capitalisation between the two copies;
// the scenarios and expected answers are identical.)

import (
	"fmt"
	"strings"
	"testing"
	"time"

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
		if m.existingCycleID != "existA" {
			t.Errorf("matched cycle = %s, want existA", m.existingCycleID)
		}
		if m.combinedTrays != 5 { // 2 existing + 3 new
			t.Errorf("combined trays = %d, want 5", m.combinedTrays)
		}
		if m.weekIndex != 0 {
			t.Errorf("week index = %d, want 0", m.weekIndex)
		}
		if m.sowDateDisplay != "Mon Jul 20" {
			t.Errorf("display date = %q, want %q", m.sowDateDisplay, "Mon Jul 20")
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
		newCycles := []farm.Cycle{
			{CropName: "sunnies", SowDate: "2026-08-10"}, // week 0 — no match
			{CropName: "sunnies", SowDate: "2026-07-27"}, // week 1 — matches existB
			{CropName: "sunnies", SowDate: "2026-08-24"}, // week 2 — no match
		}
		matches := findConsolidations(existing, newCycles, 3)
		if len(matches) != 1 {
			t.Fatalf("expected 1 match, got %d", len(matches))
		}
		if matches[0].weekIndex != 1 || matches[0].existingCycleID != "existB" {
			t.Errorf("got week %d / cycle %s, want week 1 / existB",
				matches[0].weekIndex, matches[0].existingCycleID)
		}
		if matches[0].combinedTrays != 7 { // 4 existing + 3 new
			t.Errorf("combined trays = %d, want 7", matches[0].combinedTrays)
		}
	})

	t.Run("only one existing cycle merges per new cycle", func(t *testing.T) {
		doubled := append([]farm.Cycle{}, existing...)
		doubled = append(doubled, farm.Cycle{
			CycleID: "existA2", CropName: "sunnies", Trays: 1, SowDate: "2026-07-20",
		})
		newCycles := []farm.Cycle{{CropName: "sunnies", SowDate: "2026-07-20"}}
		matches := findConsolidations(doubled, newCycles, 3)
		if len(matches) != 1 || matches[0].existingCycleID != "existA" {
			t.Fatalf("expected a single match with the first existing cycle, got: %+v", matches)
		}
	})
}

// TestParseDate covers the flexible date entry every CLI prompt uses:
// MM-DD shorthand (current year assumed), full YYYY-MM-DD, and clear errors
// for anything else. The MM-DD case uses whatever year it is when the test
// runs, because that is exactly what parseDate itself does.
func TestParseDate(t *testing.T) {
	thisYear := time.Now().Year()

	t.Run("MM-DD gets the current year", func(t *testing.T) {
		got, err := parseDate("03-15")
		if err != nil {
			t.Fatal(err)
		}
		want := fmt.Sprintf("%d-03-15", thisYear)
		if got != want {
			t.Errorf("parseDate(\"03-15\") = %s, want %s", got, want)
		}
	})

	t.Run("full dates pass through unchanged", func(t *testing.T) {
		got, err := parseDate("2027-01-03")
		if err != nil {
			t.Fatal(err)
		}
		if got != "2027-01-03" {
			t.Errorf("full date changed to %s", got)
		}
	})

	t.Run("surrounding spaces are forgiven", func(t *testing.T) {
		got, err := parseDate("  2026-06-01 ")
		if err != nil || got != "2026-06-01" {
			t.Errorf("got %q, %v — want 2026-06-01 with no error", got, err)
		}
	})

	t.Run("nonsense is rejected with a helpful error", func(t *testing.T) {
		for _, bad := range []string{"13-45", "hello", "2026/03/15", ""} {
			if _, err := parseDate(bad); err == nil {
				t.Errorf("parseDate(%q) should be an error, but was accepted", bad)
			} else if !strings.Contains(err.Error(), "MM-DD") {
				t.Errorf("error for %q should mention the expected format, got: %v", bad, err)
			}
		}
	})
}
