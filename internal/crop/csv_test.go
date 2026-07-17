package crop

// Automated tests for the crop CSV reader and writer.
// Run with "go test ./..." from the project root.
//
// Why these tests matter: crops.csv IS the database. A scheduler bug wastes
// one cycle; a bug that writes this file wrong destroys the grower's data
// permanently (this project has already been bitten once — see
// internal/trial/promote_test.go for that story). The core promise tested
// here is the round trip: whatever WriteCrops saves, LoadCrops must read
// back EXACTLY, decimals and all.

import (
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
)

// fixtureCrops returns two fully-populated crops — every column exercised,
// including awkward values: a Day 0 row, fractional weights (113.4 g),
// fractional prices ($4.50), and empty task cells.
//
// CycleDays is set to the last day number because LoadCrops derives it from
// the day rows — the round-trip comparison must expect the derived value.
func fixtureCrops() []Crop {
	return []Crop{
		{
			Name: "sunnies", CycleDays: 5,
			OvernightSoak: false, SoakHours: 4, SeedGrams: 150,
			MediumLitres: 1, SaturateMl: 800, DarkDays: 2, LightDays: 1,
			YieldGrams: 500, SeedCost: 15, SeedPurchaseWeight: 500,
			UnitWeight: 100, UnitSellPrice: 4.5,
			Days: []CropDay{
				{Day: 1, Stage: "sow", Tasks: "sow seed, saturate dirt"},
				{Day: 2, Stage: "dark", Tasks: "top water, rotate stack"},
				{Day: 3, Stage: "dark", Tasks: "bottom water, unstack"},
				{Day: 4, Stage: "light", Tasks: "move to lit rack"},
				{Day: 5, Stage: "harvest", Tasks: "harvest, wash"},
			},
		},
		{
			Name: "pea", CycleDays: 4,
			OvernightSoak: true, SoakHours: 12, SeedGrams: 250,
			MediumLitres: 1.25, SaturateMl: 600, DarkDays: 1, LightDays: 1,
			YieldGrams: 400, SeedCost: 12.5, SeedPurchaseWeight: 1000,
			UnitWeight: 113.4, UnitSellPrice: 5,
			Days: []CropDay{
				{Day: 0, Stage: "sow", Tasks: "measure seed, soak overnight"},
				{Day: 1, Stage: "sow", Tasks: "drain seed, sow seed"},
				{Day: 2, Stage: "dark", Tasks: "bottom water"},
				{Day: 3, Stage: "light", Tasks: ""}, // do-nothing day — must survive as empty
				{Day: 4, Stage: "harvest", Tasks: "harvest"},
			},
		},
	}
}

// TestWriteLoadRoundTrip — the fundamental promise: save then load returns
// identical data. If any column drifts out of place, or any decimal is
// mangled, this fails immediately.
func TestWriteLoadRoundTrip(t *testing.T) {
	path := filepath.Join(t.TempDir(), "crops.csv")
	want := fixtureCrops()

	if err := WriteCrops(path, want); err != nil {
		t.Fatalf("WriteCrops: %v", err)
	}
	got, err := (CSVSource{Path: path}).LoadCrops()
	if err != nil {
		t.Fatalf("LoadCrops: %v", err)
	}

	// reflect.DeepEqual compares every field of every crop and every day.
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("round trip changed the data.\nwrote: %+v\nread:  %+v", want, got)
	}
}

// TestModifyCropDaysAddAndRemove — the day-adjustment feature rewrites
// crops.csv in place, renumbering every later day. Renumbering is exactly
// the kind of fiddly index arithmetic that breaks when edited, so this
// walks a crop through an add, a remove, and the "can't remove below 1 day"
// guard rail.
func TestModifyCropDaysAddAndRemove(t *testing.T) {
	path := filepath.Join(t.TempDir(), "crops.csv")
	if err := WriteCrops(path, fixtureCrops()); err != nil {
		t.Fatal(err)
	}

	// ── Add 2 dark days to sunnies (2 dark → 4 dark, 5-day → 7-day cycle) ──
	if err := ModifyCropDays(path, "sunnies", "dark", 2); err != nil {
		t.Fatalf("adding dark days: %v", err)
	}
	crops, err := (CSVSource{Path: path}).LoadCrops()
	if err != nil {
		t.Fatalf("reload after add: %v", err)
	}
	sunnies := crops[0]

	if sunnies.DarkDays != 4 {
		t.Errorf("dark_days in the header row = %d, want 4", sunnies.DarkDays)
	}
	if sunnies.CycleDays != 7 {
		t.Errorf("cycle length = %d days, want 7", sunnies.CycleDays)
	}
	// Day numbers must stay consecutive (1,2,3…) and stages in order:
	// sow, then 4 dark, then light, then harvest.
	wantStages := []string{"sow", "dark", "dark", "dark", "dark", "light", "harvest"}
	for i, d := range sunnies.Days {
		if d.Day != i+1 {
			t.Errorf("row %d: day number = %d, want %d (renumbering broke)", i, d.Day, i+1)
		}
		if d.Stage != wantStages[i] {
			t.Errorf("day %d: stage = %q, want %q", d.Day, d.Stage, wantStages[i])
		}
	}
	// The inserted rows copy the last dark day's tasks with a reminder to
	// review them in the spreadsheet.
	if !strings.Contains(sunnies.Days[3].Tasks, "adjusted - be mindful") {
		t.Errorf("inserted day should carry the review reminder, got tasks: %q", sunnies.Days[3].Tasks)
	}
	// The OTHER crop in the file must be completely untouched.
	if !reflect.DeepEqual(crops[1], fixtureCrops()[1]) {
		t.Errorf("pea was altered by a sunnies-only modification: %+v", crops[1])
	}

	// ── Remove 3 dark days (4 dark → 1 dark, back to a 4-day cycle) ──────
	if err := ModifyCropDays(path, "sunnies", "dark", -3); err != nil {
		t.Fatalf("removing dark days: %v", err)
	}
	crops, err = (CSVSource{Path: path}).LoadCrops()
	if err != nil {
		t.Fatalf("reload after remove: %v", err)
	}
	sunnies = crops[0]
	if sunnies.DarkDays != 1 || sunnies.CycleDays != 4 {
		t.Errorf("after removal: dark_days=%d cycle=%d, want 1 and 4", sunnies.DarkDays, sunnies.CycleDays)
	}

	// ── Guard rail: a stage can never be reduced below 1 day ─────────────
	if err := ModifyCropDays(path, "sunnies", "dark", -1); err == nil {
		t.Error("removing the last dark day should be refused, but succeeded")
	}

	// ── Guard rail: unknown crop names are a clear error ─────────────────
	if err := ModifyCropDays(path, "nonexistent", "dark", 1); err == nil {
		t.Error("modifying a crop that doesn't exist should be an error")
	}
}

// TestAppendCropRejectsDuplicates — two crops with the same name would
// confuse the scheduler (it wouldn't know which to use), so AppendCrop must
// refuse, case-insensitively.
func TestAppendCropRejectsDuplicates(t *testing.T) {
	// AppendCrop works on the real ~/.greenies path, so give it a throwaway
	// HOME for the duration of this test. The .greenies folder must exist —
	// in real use it is created on first run, before any crop is added.
	home := t.TempDir()
	t.Setenv("HOME", home)
	if err := os.MkdirAll(filepath.Join(home, ".greenies"), 0755); err != nil {
		t.Fatal(err)
	}

	first := fixtureCrops()[0]
	if err := AppendCrop(first); err != nil {
		t.Fatalf("first append should succeed: %v", err)
	}

	dupe := first
	dupe.Name = "SUNNIES" // different capitalisation, same crop
	if err := AppendCrop(dupe); err == nil {
		t.Error("appending a duplicate crop name should be refused, but succeeded")
	}
}
