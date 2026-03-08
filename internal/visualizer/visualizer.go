// Package visualizer produces the text display for "greenies snapshot".
//
// Think of this package as the display screen for the farm. It takes a list
// of environments (from farm.csv) and a list of active cycle records (from
// cycles.json), figures out where every batch of trays is right now, and
// prints a tidy sectioned view.
//
// No data is written here — this package is purely about presentation.
package visualizer

import (
	"fmt"
	"strings" // for strings.Repeat (the separator line)
	"time"

	"github.com/littleguygreens/greenies/internal/farm"
	"github.com/littleguygreens/greenies/internal/task"
)

// ─────────────────────────────────────────────────────────────────────────────
// Status constants
// ─────────────────────────────────────────────────────────────────────────────

// These constants name the five possible states a cycle can be in relative to
// today's date. Only three of them are ever shown in the snapshot —
// "completed" and "tooFar" are silently excluded.
const (
	statusBlackout  = "blackout"  // trays are in the blackout room right now
	statusLit       = "lit"       // trays are on a lit rack (includes harvest day)
	statusUpcoming  = "upcoming"  // sow date is within the next 7 days
	statusCompleted = "completed" // harvest date has already passed
	statusTooFar    = "too_far"   // sow date is more than 7 days away — not yet relevant
)

// ─────────────────────────────────────────────────────────────────────────────
// Classification helpers
// ─────────────────────────────────────────────────────────────────────────────

// cycleStatus works out which section of the snapshot a cycle belongs to,
// based on where today's date falls relative to the cycle's key dates.
//
// The rules, in priority order:
//   - today > harvest         → completed (hide entirely)
//   - today >= moveToLight    → lit (on a lit rack, including harvest day)
//   - today >= sow            → blackout (trays are being germinated / in dark)
//   - today >= sow − 7 days  → upcoming (starting within the week)
//   - earlier than that       → too far away (hide entirely)
func cycleStatus(today, sow, moveToLight, harvest time.Time) string {
	// today.After(harvest) means today is strictly past the harvest day.
	if today.After(harvest) {
		return statusCompleted
	}
	// !today.Before(moveToLight) means today >= moveToLight.
	if !today.Before(moveToLight) {
		return statusLit
	}
	// !today.Before(sow) means today >= sow.
	if !today.Before(sow) {
		return statusBlackout
	}
	// today < sow. Check whether the sow date is within the next 7 days.
	// sow.AddDate(0, 0, -7) is "7 days before the sow date".
	if !today.Before(sow.AddDate(0, 0, -7)) {
		return statusUpcoming
	}
	return statusTooFar
}

// ─────────────────────────────────────────────────────────────────────────────
// Display helpers
// ─────────────────────────────────────────────────────────────────────────────

// trayWord returns "tray" for exactly one tray and "trays" for any other count.
// Used to make the tray count read naturally in English.
func trayWord(n int) string {
	if n == 1 {
		return "tray"
	}
	return "trays"
}

// capitalize uppercases the first letter of a string and leaves the rest alone.
// Used to display crop names from cycles.json (which are lowercase) with a
// capital at the start of a line.
//
// Note: copies of this helper also exist in main.go and internal/scheduler.
// Go does not allow sharing unexported (lowercase) helpers across packages, so
// each package keeps its own copy.
func capitalize(s string) string {
	if s == "" {
		return ""
	}
	return strings.ToUpper(s[:1]) + s[1:]
}

// printCycleRow prints a single active cycle as one formatted line, like:
//
//	  Sunnies       2 trays   sown Mar 03   harvest Mar 11   Day 4 (dark)
//
// The day label (e.g. "Day 4 (dark)") is computed from today's date relative
// to the cycle's key dates.
func printCycleRow(c farm.Cycle, today, sow, moveToLight, harvest time.Time) {
	// Work out what day number today is in this cycle.
	// Day 1 is the sow date, so the day number = days since sow + 1.
	// Both today and sow are at midnight, so the subtraction is clean.
	dayNum := int(today.Sub(sow).Hours()/24) + 1

	// Build the human-readable stage label for today.
	var stageLabel string
	if today.Equal(harvest) {
		stageLabel = fmt.Sprintf("Day %d (harvest!)", dayNum)
	} else if today.Before(moveToLight) {
		// Still in the blackout room.
		if dayNum == 1 {
			stageLabel = "Day 1 (sow)"
		} else {
			stageLabel = fmt.Sprintf("Day %d (dark)", dayNum)
		}
	} else {
		// On a lit rack.
		stageLabel = fmt.Sprintf("Day %d (light)", dayNum)
	}

	trayLabel := fmt.Sprintf("%d %s", c.Trays, trayWord(c.Trays))

	fmt.Printf("  %-12s  %-9s  sown %s   harvest %s   %s\n",
		capitalize(c.CropName),
		trayLabel,
		sow.Format("Jan 02"),
		harvest.Format("Jan 02"),
		stageLabel)
}

// ─────────────────────────────────────────────────────────────────────────────
// Main display function
// ─────────────────────────────────────────────────────────────────────────────

// PrintSnapshot prints the full farm snapshot to the terminal.
//
// It shows each environment (blackout room first, then lit environments in the
// order they appear in farm.csv), lists every active cycle currently in that
// environment, and finishes with a "Due for harvest today" reminder and an
// "Upcoming (next 7 days)" section for cycles about to start.
//
// Each cycle is always its own row — two batches of the same crop are two rows.
// Cycles that finished yesterday or earlier do not appear at all.
func PrintSnapshot(envs []farm.Environment, cycles []farm.Cycle, today time.Time) {
	// Strip the time-of-day from today so all comparisons are date-only.
	// We use time.UTC here because time.Parse (used to read stored date strings)
	// always produces UTC midnight. Using the local timezone for "today" would
	// cause mismatches on machines that are behind UTC — e.g. on a PST machine,
	// midnight PST is 8 AM UTC, which would make today appear "after" midnight
	// UTC on the same calendar date and incorrectly mark cycles as completed.
	t := time.Date(today.Year(), today.Month(), today.Day(), 0, 0, 0, 0, time.UTC)

	// The separator is a horizontal line that divides the sections visually.
	sep := strings.Repeat("─", 53)

	// ── Step 1: classify each cycle ──────────────────────────────────────────
	//
	// Parse the stored date strings into time.Time values once, here, rather
	// than re-parsing them in every helper that needs them. Store the results
	// in a struct alongside the cycle so we can pass everything together.

	type classifiedCycle struct {
		cycle      farm.Cycle
		status     string
		sow        time.Time
		moveToLight time.Time
		harvest    time.Time
	}

	var classified []classifiedCycle

	for _, c := range cycles {
		// time.Parse returns an error for a bad date string; we ignore it here
		// because the dates were written by the program itself and should always
		// be valid. A real corruption would surface as a garbled display.
		sow, _ := time.Parse(task.DateFormat, c.SowDate)
		mlt, _ := time.Parse(task.DateFormat, c.MoveToLightDate)
		harv, _ := time.Parse(task.DateFormat, c.HarvestDate)

		status := cycleStatus(t, sow, mlt, harv)

		// Completed and too-far cycles are excluded from the snapshot entirely.
		if status == statusCompleted || status == statusTooFar {
			continue
		}

		classified = append(classified, classifiedCycle{c, status, sow, mlt, harv})
	}

	// ── Step 2: resolve "either" lit-stage cycles ─────────────────────────────
	//
	// "Either" means the grower hadn't picked a specific tent at plan time.
	// We assign each "either" cycle to the first lit environment with enough
	// free slots, spilling to the next if that one is full.

	// First, count how many slots are already spoken for by non-"either" cycles.
	// (A cycle in blackout doesn't consume lit slots yet.)
	litUsage := map[string]int{} // environment name → slots in use
	for _, cc := range classified {
		if cc.status == statusLit && cc.cycle.LitEnvironment != "either" {
			litUsage[cc.cycle.LitEnvironment] += cc.cycle.Trays
		}
	}

	// Collect the lit environments in config order for the resolution loop.
	var litEnvs []farm.Environment
	for _, e := range envs {
		if e.Type == "lit" {
			litEnvs = append(litEnvs, e)
		}
	}

	// resolvedEnv maps a cycle's CycleID to the env name it was assigned to.
	// Only populated for "either" cycles — everyone else uses their stored value.
	resolvedEnv := map[string]string{}

	for _, cc := range classified {
		if cc.status != statusLit || cc.cycle.LitEnvironment != "either" {
			continue
		}
		// Try each lit env in order, pick the first with room.
		assigned := ""
		for _, e := range litEnvs {
			if litUsage[e.Name]+cc.cycle.Trays <= e.Capacity {
				assigned = e.Name
				break
			}
		}
		// If every env is over capacity, assign to the first one anyway.
		// The conflict checker (Phase 4) will flag it.
		if assigned == "" && len(litEnvs) > 0 {
			assigned = litEnvs[0].Name
		}
		resolvedEnv[cc.cycle.CycleID] = assigned
		litUsage[assigned] += cc.cycle.Trays
	}

	// effectiveEnv returns the lit environment name for a cycle, resolving
	// "either" using the table we just built.
	effectiveEnv := func(cc classifiedCycle) string {
		if cc.cycle.LitEnvironment == "either" {
			return resolvedEnv[cc.cycle.CycleID]
		}
		return cc.cycle.LitEnvironment
	}

	// ── Step 3: print the date header ────────────────────────────────────────

	fmt.Printf("Farm snapshot — %s\n", today.Format("Monday 2006-01-02"))

	// ── Step 4: print each environment section ────────────────────────────────

	for _, env := range envs {
		fmt.Println(sep)

		// Find cycles that currently belong to this environment.
		var envCycles []classifiedCycle

		if env.Type == "blackout" {
			// The blackout room holds every cycle that is in the germination
			// or dark stage, regardless of where it is headed afterward.
			for _, cc := range classified {
				if cc.status == statusBlackout {
					envCycles = append(envCycles, cc)
				}
			}
		} else {
			// Lit environments only hold cycles that have moved out of blackout
			// AND have not yet reached harvest day.
			//
			// On harvest day, the trays are taken off the rack to be cut and
			// washed — so those slots are immediately free for new trays moving
			// in. Harvest-day cycles are shown in their own section below instead.
			for _, cc := range classified {
				if cc.status == statusLit && effectiveEnv(cc) == env.Name && !t.Equal(cc.harvest) {
					envCycles = append(envCycles, cc)
				}
			}
		}

		// Count how many slots are currently in use in this environment.
		slotsUsed := 0
		for _, cc := range envCycles {
			slotsUsed += cc.cycle.Trays
		}

		// Print the environment header.
		if len(envCycles) == 0 {
			fmt.Printf("%-22s  empty\n", farm.DisplayName(env.Name))
		} else {
			fmt.Printf("%-22s  %d / %d slots\n",
				farm.DisplayName(env.Name), slotsUsed, env.Capacity)
			fmt.Println()
			for _, cc := range envCycles {
				printCycleRow(cc.cycle, t, cc.sow, cc.moveToLight, cc.harvest)
			}
			fmt.Println()
		}
	}

	// ── Step 5: "Due for harvest today" section ───────────────────────────────
	//
	// Cycles whose harvest date is exactly today appear in their lit environment
	// section above (they still occupy physical space) AND appear here as a
	// reminder that they need to be cut and washed today.

	var harvestToday []classifiedCycle
	for _, cc := range classified {
		if t.Equal(cc.harvest) {
			harvestToday = append(harvestToday, cc)
		}
	}

	if len(harvestToday) > 0 {
		fmt.Println(sep)
		fmt.Println("Due for harvest today")
		fmt.Println()
		for _, cc := range harvestToday {
			trayLabel := fmt.Sprintf("%d %s", cc.cycle.Trays, trayWord(cc.cycle.Trays))
			fmt.Printf("  %-12s  %-9s  harvest %s\n",
				capitalize(cc.cycle.CropName),
				trayLabel,
				cc.harvest.Format("Jan 02"))
		}
		fmt.Println()
	}

	// ── Step 6: "Upcoming" section ───────────────────────────────────────────
	//
	// Cycles whose sow date is between tomorrow and 7 days from now appear
	// here so the grower can see what's coming up without hunting through
	// the full calendar. Cycles starting more than 7 days away are excluded
	// to avoid flooding the snapshot with distant future plans.

	var upcoming []classifiedCycle
	for _, cc := range classified {
		if cc.status == statusUpcoming {
			upcoming = append(upcoming, cc)
		}
	}

	if len(upcoming) > 0 {
		fmt.Println(sep)
		fmt.Println("Upcoming (next 7 days)")
		fmt.Println()
		for _, cc := range upcoming {
			trayLabel := fmt.Sprintf("%d %s", cc.cycle.Trays, trayWord(cc.cycle.Trays))
			fmt.Printf("  %-12s  %-9s  sow %s\n",
				capitalize(cc.cycle.CropName),
				trayLabel,
				cc.sow.Format("Jan 02"))
		}
		fmt.Println()
	}

	// ── Step 7: Tray inventory section ───────────────────────────────────────
	//
	// Grow trays and bottom trays are physical objects with a fixed total count
	// (how many the farm owns). This section shows how many are currently out
	// on the farm vs. sitting in the cupboard and ready for the next sow.
	//
	// The totals come from "inventory" rows in farm.csv.
	// The "in use" counts are computed from the active cycle records:
	//
	//   Grow trays in use   = every active cycle (blackout or lit),
	//                         EXCEPT harvest day — the grower cuts first thing
	//                         in the morning so those slots are immediately free.
	//
	//   Bottom trays in use = blackout cycles only — bottom trays are returned
	//                         to the cupboard the moment trays move to light.
	//
	// If the farm.csv file has no inventory rows (e.g. it was created before
	// this feature was added), this section is silently skipped rather than
	// showing confusing zeroes.

	// Scan the environment list for inventory rows and collect their totals.
	growTotal := 0
	bottomTotal := 0
	hasInventory := false
	for _, e := range envs {
		if e.Type == "inventory" {
			hasInventory = true
			switch e.Name {
			case "grow_trays":
				growTotal = e.Capacity
			case "bottom_trays":
				bottomTotal = e.Capacity
			}
		}
	}

	if hasInventory {
		// Count how many of each tray type are currently out on the farm.
		growInUse := 0
		bottomInUse := 0
		for _, cc := range classified {
			isHarvestDay := t.Equal(cc.harvest)
			// Grow trays: occupied during blackout and light stages,
			// but free on harvest day (first-thing-in-the-morning rule).
			if (cc.status == statusBlackout || cc.status == statusLit) && !isHarvestDay {
				growInUse += cc.cycle.Trays
			}
			// Bottom trays: occupied during blackout only.
			if cc.status == statusBlackout {
				bottomInUse += cc.cycle.Trays
			}
		}

		fmt.Println(sep)
		fmt.Println("Tray inventory")
		fmt.Println()
		if growTotal > 0 {
			fmt.Printf("  %-14s  %d in use / %d total   → %d available\n",
				"Grow trays", growInUse, growTotal, growTotal-growInUse)
		}
		if bottomTotal > 0 {
			fmt.Printf("  %-14s  %d in use / %d total   → %d available\n",
				"Bottom trays", bottomInUse, bottomTotal, bottomTotal-bottomInUse)
		}
		fmt.Println()
	}
}
