package trial

// This is an automated test (a small program that checks other code still
// works — run with "go test ./..."). It guards against a real bug we once
// had: promoting a trial used to write crops.csv cells by POSITION, and when
// a new column (saturate_ml) was added to the file layout, the promoted
// values landed under the wrong headings — dark days ended up in the
// saturate_ml column, and fractional soak hours could make the whole crop
// library refuse to load.
//
// The test recreates that exact scenario: a modern 16-column crops.csv, a
// promoted trial appended to it, then a reload to prove every value landed
// under its correct named column. If someone reintroduces positional
// writing, this test fails immediately.

import (
	"path/filepath"
	"testing"

	"github.com/littleguygreens/greenies/internal/crop"
)

func TestPromoteIntoModernCropsCSV(t *testing.T) {
	dir := t.TempDir()
	cropsPath := filepath.Join(dir, "crops.csv")

	// Write an existing library with the modern 16-column layout.
	existing := []crop.Crop{{
		Name: "sunnies", SoakHours: 4, SeedGrams: 150, MediumLitres: 1,
		SaturateMl: 800, DarkDays: 4, LightDays: 3, YieldGrams: 500,
		UnitWeight: 100,
		Days: []crop.CropDay{
			{Day: 1, Stage: "sow", Tasks: "sow seed"},
			{Day: 2, Stage: "dark", Tasks: "top water"},
		},
	}}
	if err := crop.WriteCrops(cropsPath, existing); err != nil {
		t.Fatalf("WriteCrops: %v", err)
	}

	// Promote a trial: 2 trays, 610g total yield, fractional soak hours.
	tr := TrialRecord{
		CropName: "mustard", Trays: 2, ActualYieldGrams: 610,
		OvernightSoak: false, SoakHours: 4.5, SeedGrams: 30.4,
		ConfirmedDays: []TrialDayParams{
			{Day: 1, Stage: "sow", Tasks: "sow seed"},
			{Day: 2, Stage: "dark", Tasks: "mist"},
			{Day: 3, Stage: "dark", Tasks: "mist"},
			{Day: 4, Stage: "light", Tasks: "move to light"},
			{Day: 5, Stage: "harvest", Tasks: "harvest"},
		},
	}
	if err := AppendToCropsCSV(cropsPath, tr); err != nil {
		t.Fatalf("AppendToCropsCSV: %v", err)
	}

	// The library must still load, with both crops intact.
	loaded, err := (crop.CSVSource{Path: cropsPath}).LoadCrops()
	if err != nil {
		t.Fatalf("LoadCrops after promote: %v", err)
	}
	if len(loaded) != 2 {
		t.Fatalf("expected 2 crops, got %d", len(loaded))
	}

	m := loaded[1]
	if m.Name != "mustard" {
		t.Fatalf("expected mustard, got %q", m.Name)
	}
	// The old positional writer put dark days into saturate_ml, light days
	// into dark_days, and yield into light_days. Verify every value landed
	// under its correct named column.
	if m.DarkDays != 2 || m.LightDays != 1 {
		t.Errorf("dark/light days misaligned: dark=%d light=%d (want 2/1)", m.DarkDays, m.LightDays)
	}
	if m.SaturateMl != 0 {
		t.Errorf("saturate_ml should be 0 (untracked in trials), got %d", m.SaturateMl)
	}
	if m.YieldGrams != 305 {
		t.Errorf("yield per tray = %d, want 305 (610g / 2 trays)", m.YieldGrams)
	}
	if m.SoakHours != 5 {
		t.Errorf("soak hours = %d, want 5 (4.5 rounded)", m.SoakHours)
	}
	if m.SeedGrams != 30 {
		t.Errorf("seed grams = %d, want 30 (30.4 rounded)", m.SeedGrams)
	}
	if m.CycleDays != 5 {
		t.Errorf("cycle days = %d, want 5", m.CycleDays)
	}
	if len(m.Days) != 5 {
		t.Errorf("day rows = %d, want 5", len(m.Days))
	}

	// The pre-existing crop must be untouched.
	s := loaded[0]
	if s.Name != "sunnies" || s.SaturateMl != 800 || s.DarkDays != 4 || s.YieldGrams != 500 {
		t.Errorf("existing sunnies crop was altered: %+v", s)
	}
}
