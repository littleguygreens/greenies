package visualizer

// Automated tests for cycleStatus — the little function that decides which
// section of the farm snapshot every cycle appears in. Run with
// "go test ./..." from the project root.
//
// Why this matters: cycleStatus is nothing but boundary comparisons, and
// boundaries are where off-by-one bugs live. Is a tray "in blackout" or
// "on the lit rack" ON move-to-light day? Is it still occupying a slot ON
// harvest day? The farm rules in CLAUDE.md answer each of these precisely;
// this table pins the code to those answers.

import (
	"testing"
	"time"

	"github.com/littleguygreens/greenies/internal/task"
)

func TestCycleStatus(t *testing.T) {
	// One reference cycle, dates chosen so every boundary is a distinct day:
	//   sow           = March 9
	//   move to light = March 14
	//   harvest       = March 17
	d := func(s string) time.Time {
		parsed, err := time.Parse(task.DateFormat, s)
		if err != nil {
			t.Fatalf("bad test date %q: %v", s, err)
		}
		return parsed
	}
	sow := d("2026-03-09")
	mtl := d("2026-03-14")
	harvest := d("2026-03-17")

	// Each row: "if today is X, the cycle's status must be Y".
	cases := []struct {
		today string
		want  string
		note  string
	}{
		{"2026-03-01", statusTooFar, "8 days before sow — too far out to show"},
		{"2026-03-02", statusUpcoming, "exactly 7 days before sow — first day it appears as upcoming"},
		{"2026-03-08", statusUpcoming, "the day before sow — still upcoming"},
		{"2026-03-09", statusBlackout, "sow day — trays enter the blackout room"},
		{"2026-03-13", statusBlackout, "last dark day — still in blackout"},
		{"2026-03-14", statusLit, "move-to-light day — counts as lit from this morning"},
		{"2026-03-16", statusLit, "day before harvest — still on the rack"},
		{"2026-03-17", statusLit, "harvest day — still 'lit' here; the snapshot's harvest-today section handles the display"},
		{"2026-03-18", statusCompleted, "day after harvest — gone from the snapshot"},
	}

	for _, c := range cases {
		got := cycleStatus(d(c.today), sow, mtl, harvest)
		if got != c.want {
			t.Errorf("on %s: status = %q, want %q (%s)", c.today, got, c.want, c.note)
		}
	}
}
