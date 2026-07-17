package checker

// Automated tests for the conflict checker — the "does it fit?" engine.
// Run with "go test ./..." from the project root.
//
// Why these tests matter: a wrong answer here costs real money in both
// directions. If the checker says a plan fits when the tent will actually
// overflow, the grower sows trays they cannot house. If it cries wolf with
// false warnings, the grower learns to ignore it — until a real one slips
// past. These tests pin down the exact boundary behaviour: at capacity is
// fine, one over warns, and harvest morning frees slots.

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/littleguygreens/greenies/internal/config"
	"github.com/littleguygreens/greenies/internal/farm"
)

// setTestHome points HOME at a throwaway directory so the checker's config
// lookup (flood vs tray-pairs irrigation) never reads the real
// ~/.greenies/config.json. t.Setenv restores the real HOME automatically
// when the test finishes.
func setTestHome(t *testing.T) string {
	t.Helper()
	home := t.TempDir()
	t.Setenv("HOME", home)
	return home
}

// testEnvs builds a small farm layout for these tests. Small numbers make
// the arithmetic easy to follow: an overflow of "5 into 4" is checkable in
// your head, unlike "70 into 64".
func testEnvs(mainCap, testCap, growTrays, bottomTrays int) []farm.Environment {
	return []farm.Environment{
		{Name: "blackout", Type: "blackout", Capacity: 100},
		{Name: "main_tent", Type: "lit", Capacity: mainCap},
		{Name: "test_tent", Type: "lit", Capacity: testCap},
		{Name: "grow_trays", Type: "inventory", Capacity: growTrays},
		{Name: "bottom_trays", Type: "inventory", Capacity: bottomTrays},
	}
}

// mkCycle builds one cycle record with the three dates the checker cares
// about. Dates are YYYY-MM-DD strings, same as the real cycles.json.
func mkCycle(cropName string, trays int, sow, moveToLight, harvest, litEnv string) farm.Cycle {
	return farm.Cycle{
		CycleID:         cropName + "-" + sow, // unique enough for tests
		CropName:        cropName,
		Trays:           trays,
		SowDate:         sow,
		MoveToLightDate: moveToLight,
		HarvestDate:     harvest,
		LitEnvironment:  litEnv,
	}
}

// TestNoCyclesNoWarnings — an empty farm can never have a conflict.
func TestNoCyclesNoWarnings(t *testing.T) {
	setTestHome(t)
	if warnings := Check(testEnvs(4, 3, 10, 6), nil); warnings != nil {
		t.Fatalf("expected no warnings for an empty farm, got: %v", warnings)
	}
}

// TestLitCapacityBoundary — the most important boundary in the program:
// exactly at capacity must pass silently; one tray over must warn.
func TestLitCapacityBoundary(t *testing.T) {
	setTestHome(t)
	envs := testEnvs(4, 3, 20, 20)

	// Exactly 4 trays into a 4-slot tent: fits, no warning.
	fits := []farm.Cycle{
		mkCycle("sunnies", 4, "2026-03-01", "2026-03-06", "2026-03-10", "main_tent"),
	}
	if warnings := Check(envs, fits); len(warnings) != 0 {
		t.Fatalf("4 trays in a 4-slot tent should fit, got warnings: %v", warnings)
	}

	// 5 trays into the same tent: one over, must warn and say by how much.
	over := []farm.Cycle{
		mkCycle("sunnies", 5, "2026-03-01", "2026-03-06", "2026-03-10", "main_tent"),
	}
	warnings := Check(envs, over)
	if len(warnings) != 1 {
		t.Fatalf("expected exactly 1 warning, got %d: %v", len(warnings), warnings)
	}
	if !strings.Contains(warnings[0], "Main tent") || !strings.Contains(warnings[0], "(1 over)") {
		t.Errorf("warning should name the tent and the overflow amount, got: %s", warnings[0])
	}
}

// TestGrowTrayStock — two overlapping batches can together exceed the number
// of grow trays the farm physically owns, even when each tent has room.
func TestGrowTrayStock(t *testing.T) {
	setTestHome(t)
	// Big tents (8 slots each) so no lit warning fires; only 10 grow trays
	// owned; bottom trays set to 0, which tells the checker to skip that
	// check entirely (no inventory row = nothing to check).
	envs := testEnvs(8, 8, 10, 0)

	cycles := []farm.Cycle{
		mkCycle("sunnies", 6, "2026-03-01", "2026-03-06", "2026-03-10", "main_tent"),
		mkCycle("pea", 5, "2026-03-01", "2026-03-05", "2026-03-09", "test_tent"),
	}
	// Both batches are on the farm from March 1: 6 + 5 = 11 trays in use,
	// but the farm only owns 10.
	warnings := Check(envs, cycles)
	if len(warnings) != 1 || !strings.Contains(warnings[0], "Grow tray stock") {
		t.Fatalf("expected one grow-tray stock warning, got: %v", warnings)
	}
	if !strings.Contains(warnings[0], "11/10") {
		t.Errorf("warning should show 11/10 in use, got: %s", warnings[0])
	}
}

// TestBottomTraysFloodVsTrayPairs — the two irrigation modes count bottom
// trays differently, and that difference decides whether this farm layout
// warns or not:
//
//	flood mode (default): bottom trays come back at move-to-light,
//	  so cycle A (already on the lit rack) holds none — no warning.
//	tray_pairs mode: bottom trays stay paired until harvest,
//	  so A's 4 + B's 3 = 7 > 6 owned — warning.
func TestBottomTraysFloodVsTrayPairs(t *testing.T) {
	envs := testEnvs(8, 8, 20, 6)
	cycles := []farm.Cycle{
		// A moves to light March 6 — after that its bottom trays are free
		// in flood mode, but still paired in tray_pairs mode.
		mkCycle("sunnies", 4, "2026-03-01", "2026-03-06", "2026-03-12", "main_tent"),
		// B sows March 7, while A is on the lit rack.
		mkCycle("pea", 3, "2026-03-07", "2026-03-11", "2026-03-16", "test_tent"),
	}

	// Default (flood) mode: peak bottom-tray usage is only B's 3 — fine.
	setTestHome(t)
	if warnings := Check(envs, cycles); len(warnings) != 0 {
		t.Fatalf("flood mode should not warn, got: %v", warnings)
	}

	// tray_pairs mode: write the setting into the test HOME's config.json,
	// exactly as the settings page would.
	home := setTestHome(t)
	if err := os.MkdirAll(filepath.Join(home, ".greenies"), 0755); err != nil {
		t.Fatal(err)
	}
	if err := config.Save(config.Config{IrrigationMode: "tray_pairs"}); err != nil {
		t.Fatal(err)
	}
	warnings := Check(envs, cycles)
	if len(warnings) != 1 || !strings.Contains(warnings[0], "Bottom tray stock") {
		t.Fatalf("tray_pairs mode should give one bottom-tray warning, got: %v", warnings)
	}
}

// TestAnyEnvironmentCascade — a cycle planned with environment "any" should
// be assigned to the first lit environment with room, spilling to the next
// when the first is full, and only warn when nowhere has room.
func TestAnyEnvironmentCascade(t *testing.T) {
	setTestHome(t)
	envs := testEnvs(4, 3, 20, 20)

	// main_tent already holds 3 trays; the "any" cycle needs 3 more.
	// 3+3 = 6 won't fit main_tent's 4 slots → it must cascade to test_tent
	// (3 slots, exactly enough). No warning expected.
	fits := []farm.Cycle{
		mkCycle("sunnies", 3, "2026-03-01", "2026-03-06", "2026-03-10", "main_tent"),
		mkCycle("pea", 3, "2026-03-01", "2026-03-05", "2026-03-10", "any"),
	}
	if warnings := Check(envs, fits); len(warnings) != 0 {
		t.Fatalf("\"any\" cycle should cascade to the test tent, got warnings: %v", warnings)
	}

	// Now the "any" cycle needs 4 trays — too big for either tent's free
	// space. The checker assigns it to the first tent anyway and must warn.
	over := []farm.Cycle{
		mkCycle("sunnies", 3, "2026-03-01", "2026-03-06", "2026-03-10", "main_tent"),
		mkCycle("pea", 4, "2026-03-01", "2026-03-05", "2026-03-10", "any"),
	}
	warnings := Check(envs, over)
	if len(warnings) != 1 || !strings.Contains(warnings[0], "Main tent") {
		t.Fatalf("expected a Main tent warning when no tent has room, got: %v", warnings)
	}
}

// TestHarvestMorningFreesSlots — the "first thing in the morning" rule:
// on harvest day the outgoing trays are cut before anything moves in, so a
// new batch may take those exact slots on that exact day. If the checker
// counted both batches on harvest day it would report a phantom conflict.
func TestHarvestMorningFreesSlots(t *testing.T) {
	setTestHome(t)
	envs := testEnvs(4, 3, 20, 20)

	cycles := []farm.Cycle{
		// A: 4 trays fill the main tent until harvest on March 10.
		mkCycle("sunnies", 4, "2026-03-01", "2026-03-05", "2026-03-10", "main_tent"),
		// B: 4 trays move into the main tent ON March 10 — A's harvest day.
		mkCycle("pea", 4, "2026-03-05", "2026-03-10", "2026-03-14", "main_tent"),
	}
	if warnings := Check(envs, cycles); len(warnings) != 0 {
		t.Fatalf("back-to-back batches on harvest day should not conflict, got: %v", warnings)
	}
}
