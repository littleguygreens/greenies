package scheduler

// Automated tests for the scheduler — the date-math heart of the program.
// Run with "go test ./..." from the project root.
//
// Why these tests matter: the scheduler's job is to turn "harvest sunnies on
// March 15" into nine correctly-dated calendar tasks. If it is ever off by
// one day, nothing crashes — the calendar just quietly tells the grower to
// water on the wrong day. Silent failures like that are exactly what tests
// exist to catch.
//
// These are "table-driven" tests, Go's standard style: each test declares a
// table of scenarios (inputs and the answers we expect), then loops over the
// table running the real code against each row. Adding a new scenario later
// means adding one row, not writing a new test.

import (
	"strings"
	"testing"

	"github.com/littleguygreens/greenies/internal/crop"
	"github.com/littleguygreens/greenies/internal/task"
)

// sunniesCrop returns the reference sunnies crop from CLAUDE.md:
// a 9-day cycle with no Day 0 soak. Building fixtures in code (rather than
// reading crops.csv) keeps these tests independent of any real file.
func sunniesCrop() crop.Crop {
	return crop.Crop{
		Name:       "sunnies",
		CycleDays:  9,
		SoakHours:  4,
		SeedGrams:  150,
		SaturateMl: 800,
		DarkDays:   4,
		LightDays:  3,
		YieldGrams: 500,
		Days: []crop.CropDay{
			{Day: 1, Stage: "sow", Tasks: "measure seed, soak 4 hours, drain seed, prepare trays, sow seed, saturate dirt, top water, stack"},
			{Day: 2, Stage: "dark", Tasks: "top water, rotate stack"},
			{Day: 3, Stage: "dark", Tasks: "top water, rotate stack"},
			{Day: 4, Stage: "dark", Tasks: "bottom water, top water, rotate stack"},
			{Day: 5, Stage: "dark", Tasks: "bottom water, top water, unstack"},
			{Day: 6, Stage: "light", Tasks: "move to lit rack"},
			{Day: 7, Stage: "light", Tasks: "mist, dehusk"},
			{Day: 8, Stage: "light", Tasks: "mist, dehusk"},
			{Day: 9, Stage: "harvest", Tasks: "harvest, wash"},
		},
	}
}

// peaCrop returns the reference pea crop from CLAUDE.md: an 8-day cycle with
// a Day 0 overnight soak and two do-nothing days on the lit rack.
func peaCrop() crop.Crop {
	return crop.Crop{
		Name:          "pea",
		CycleDays:     8,
		OvernightSoak: true,
		SoakHours:     12,
		SeedGrams:     250,
		DarkDays:      3,
		LightDays:     3,
		YieldGrams:    400,
		Days: []crop.CropDay{
			{Day: 0, Stage: "sow", Tasks: "measure seed, soak overnight"},
			{Day: 1, Stage: "sow", Tasks: "drain seed, prepare trays, sow seed, saturate dirt, top water"},
			{Day: 2, Stage: "dark", Tasks: "bottom water"},
			{Day: 3, Stage: "dark", Tasks: "bottom water"},
			{Day: 4, Stage: "dark", Tasks: "bottom water"},
			{Day: 5, Stage: "light", Tasks: "move to lit rack"},
			{Day: 6, Stage: "light", Tasks: ""},
			{Day: 7, Stage: "light", Tasks: ""},
			{Day: 8, Stage: "harvest", Tasks: "harvest"},
		},
	}
}

// TestScheduleBackward checks planning backward from a harvest date:
// "I want to harvest on March 15" — does every one of the nine days land on
// the right calendar date, counting back from the 15th?
func TestScheduleBackward(t *testing.T) {
	preview, tasks, err := Schedule(sunniesCrop(), "2026-03-15", 2)
	if err != nil {
		t.Fatal(err)
	}

	// Every cycle day, with the exact date each must land on.
	wantDates := []string{
		"2026-03-07", // day 1 — sow
		"2026-03-08", // day 2
		"2026-03-09", // day 3
		"2026-03-10", // day 4
		"2026-03-11", // day 5
		"2026-03-12", // day 6 — move to light
		"2026-03-13", // day 7
		"2026-03-14", // day 8
		"2026-03-15", // day 9 — harvest (the anchor date)
	}

	if len(preview) != len(wantDates) {
		t.Fatalf("expected %d preview days, got %d", len(wantDates), len(preview))
	}
	for i, want := range wantDates {
		if preview[i].Date != want {
			t.Errorf("day %d: date = %s, want %s", preview[i].CropDay.Day, preview[i].Date, want)
		}
	}

	// Every task in one planning run must share a single CycleID — that is
	// what lets "greenies delete" remove the whole cycle at once.
	if len(tasks) != 9 {
		t.Fatalf("expected 9 tasks, got %d", len(tasks))
	}
	for _, tk := range tasks {
		if tk.CycleID == "" || tk.CycleID != tasks[0].CycleID {
			t.Fatalf("all tasks should share one non-empty CycleID; got %q and %q", tasks[0].CycleID, tk.CycleID)
		}
	}

	// Titles follow "CropName Nx stage" — the format the GUI, Google sync,
	// and swim-lane calendar all parse.
	if tasks[0].Title != "Sunnies 2x sow" {
		t.Errorf("first task title = %q, want %q", tasks[0].Title, "Sunnies 2x sow")
	}
}

// TestScheduleForwardWithSoak checks planning forward from a sow date for a
// crop with a Day 0 overnight soak. The rule from CLAUDE.md: "sow date"
// always means Day 1, and the soak reminder lands on the day BEFORE it.
func TestScheduleForwardWithSoak(t *testing.T) {
	preview, tasks, err := ScheduleForward(peaCrop(), "2026-03-09", 2)
	if err != nil {
		t.Fatal(err)
	}

	wantDates := []string{
		"2026-03-08", // day 0 — soak, the day BEFORE the sow date
		"2026-03-09", // day 1 — the sow date the grower entered
		"2026-03-10", // day 2
		"2026-03-11", // day 3
		"2026-03-12", // day 4
		"2026-03-13", // day 5 — move to light
		"2026-03-14", // day 6 — do-nothing day
		"2026-03-15", // day 7 — do-nothing day
		"2026-03-16", // day 8 — harvest
	}

	if len(preview) != len(wantDates) {
		t.Fatalf("expected %d preview days, got %d", len(wantDates), len(preview))
	}
	for i, want := range wantDates {
		if preview[i].Date != want {
			t.Errorf("day %d: date = %s, want %s", preview[i].CropDay.Day, preview[i].Date, want)
		}
	}

	// Do-nothing days must still become tasks (the visualizer needs them to
	// know trays occupy rack space), marked with the NoTasksNote constant so
	// calendar sync knows to skip them.
	day6 := tasks[6] // index 6 = day 6 (index 0 is day 0)
	if !strings.Contains(day6.Notes, task.NoTasksNote) {
		t.Errorf("do-nothing day notes = %q, should contain %q", day6.Notes, task.NoTasksNote)
	}
}

// TestScheduleAcrossYearBoundary checks the December→January case: planning
// in one year, harvesting in the next. Date arithmetic that only adds days
// to a month number would break here.
func TestScheduleAcrossYearBoundary(t *testing.T) {
	preview, _, err := ScheduleForward(peaCrop(), "2026-12-30", 1)
	if err != nil {
		t.Fatal(err)
	}
	first := preview[0].Date
	last := preview[len(preview)-1].Date
	if first != "2026-12-29" {
		t.Errorf("day 0 = %s, want 2026-12-29", first)
	}
	if last != "2027-01-06" {
		t.Errorf("harvest = %s, want 2027-01-06 (crossed into the new year)", last)
	}
}

// TestScheduleFromDay checks mid-cycle regeneration — the engine behind
// "greenies adjust". Only the days from fromDayNum onward should be
// generated, and every task must carry the EXISTING CycleID so the
// regenerated tasks stay linked to the original cycle record.
func TestScheduleFromDay(t *testing.T) {
	tasks, err := ScheduleFromDay(sunniesCrop(), "2026-03-07", 6, 3, "existing-cycle-id")
	if err != nil {
		t.Fatal(err)
	}

	// Days 6, 7, 8, 9 remain — days 1-5 are in the past and skipped.
	if len(tasks) != 4 {
		t.Fatalf("expected 4 tasks (days 6-9), got %d", len(tasks))
	}
	if tasks[0].Date != "2026-03-12" {
		t.Errorf("day 6 date = %s, want 2026-03-12 (sow date + 5 days)", tasks[0].Date)
	}
	if tasks[3].Date != "2026-03-15" {
		t.Errorf("day 9 date = %s, want 2026-03-15", tasks[3].Date)
	}
	for _, tk := range tasks {
		if tk.CycleID != "existing-cycle-id" {
			t.Fatalf("task CycleID = %q, want the existing ID passed in", tk.CycleID)
		}
	}
}

// TestTaskNoteExpansions checks the note substitutions that save the grower
// mental arithmetic: "measure seed" becomes the TOTAL weight for the batch,
// "sow seed" shows the PER-TRAY rate, and "saturate dirt" shows the water
// volume — all pulled from the crop's parameters.
func TestTaskNoteExpansions(t *testing.T) {
	// 2 trays of sunnies at 150 g/tray and 800 mL saturate water.
	_, tasks, err := Schedule(sunniesCrop(), "2026-03-15", 2)
	if err != nil {
		t.Fatal(err)
	}
	sowNotes := tasks[0].Notes

	wantPhrases := []string{
		"measure 300g seed",     // 2 trays × 150 g — total, weighed once
		"sow seed - 150g/tray",  // per tray, because sowing happens tray by tray
		"saturate dirt - 800mL", // from the crop's saturate_ml parameter
		"2 trays",               // tray count prefix on every note
	}
	for _, phrase := range wantPhrases {
		if !strings.Contains(sowNotes, phrase) {
			t.Errorf("sow-day notes missing %q\nfull notes: %s", phrase, sowNotes)
		}
	}
}
