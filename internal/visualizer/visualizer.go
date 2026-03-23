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
	"io"
	"os"
	"strings" // for strings.Repeat (the separator line) and strings.Builder
	"time"

	"github.com/littleguygreens/greenies/internal/checker"
	"github.com/littleguygreens/greenies/internal/config"
	"github.com/littleguygreens/greenies/internal/farm"
	"github.com/littleguygreens/greenies/internal/task"
)

// ─────────────────────────────────────────────────────────────────────────────
// Structured snapshot data
// ─────────────────────────────────────────────────────────────────────────────
//
// These types hold the same information that writeSnapshot() prints as text,
// but in a structured form that the GUI can use to render styled HTML cards,
// progress bars, and colour-coded stages — instead of a plain <pre> block.

// SnapshotData is the top-level container returned by BuildSnapshot().
// It holds everything the GUI needs to render a full farm snapshot page.
type SnapshotData struct {
	// DateLabel is a short human-readable date like "Wed 03-19".
	DateLabel string

	// Environments is one entry per physical space on the farm (blackout room,
	// main tent, test tent, etc.) — in the same order as farm.csv.
	// Inventory rows (grow_trays, bottom_trays) are NOT included here.
	Environments []EnvSection

	// HarvestToday lists cycles whose harvest date is exactly today.
	// These are shown as a separate reminder section.
	HarvestToday []CycleRow

	// Upcoming lists cycles whose sow date is within the next 7 days
	// but hasn't arrived yet — a heads-up for the grower.
	Upcoming []CycleRow

	// Inventory holds tray stock information (grow trays, bottom trays).
	// Empty if farm.csv has no inventory rows.
	Inventory []InventoryItem

	// Conflicts is a list of human-readable conflict warnings from the
	// conflict checker (e.g. "Main Tent over capacity on 2026-03-22").
	Conflicts []string

	// Financial holds the aggregated cost/revenue/profit for cycles harvesting
	// this calendar week. Filled in by the GUI handler after BuildSnapshot
	// returns — the visualizer itself stays independent of crop/supply packages.
	Financial FinancialSummary

	// HasData is true if there was at least one environment or cycle to show.
	// The template uses this to decide whether to show the snapshot or an
	// "empty" message.
	HasData bool
}

// FinancialSummary holds aggregated profitability numbers for cycles
// harvesting in the current calendar week (Mon–Sun). The GUI handler
// populates this after calling BuildSnapshot() by filtering cycles by
// harvest date, looking up each crop's costing data, and using
// SellableUnits (floor of total units) for revenue.
type FinancialSummary struct {
	// TotalCost is the sum of (cost per tray × trays) for all cycles with data.
	TotalCost float64

	// TotalRevenue is the sum of (revenue per tray × trays) for all cycles.
	TotalRevenue float64

	// TotalProfit is TotalRevenue minus TotalCost.
	TotalProfit float64

	// HasData is true if at least one active cycle had costing data.
	HasData bool
}

// EnvSection holds the data for one physical environment (blackout room or
// a lit tent). Think of it as one "card" in the GUI snapshot.
type EnvSection struct {
	// Name is the human-readable name like "Blackout" or "Main Tent".
	Name string

	// Type is "blackout" or "lit" — used by the template to pick a colour.
	Type string

	// SlotsUsed is how many tray slots are currently occupied.
	SlotsUsed int

	// Capacity is the total number of slots this environment has.
	Capacity int

	// Cycles lists every batch of trays currently in this environment.
	// Empty if the environment is currently unused.
	Cycles []CycleRow
}

// CycleRow holds the display data for one active crop cycle (one batch of
// trays). This is the structured version of what printCycleRow() prints.
type CycleRow struct {
	// CropName is the human-readable crop name like "Sunnies" (capitalised).
	CropName string

	// Trays is the number of grow trays in this batch.
	Trays int

	// DayNum is the current day number in the cycle (Day 1 = sow day).
	DayNum int

	// Stage is a short label: "sow", "dark", "light", or "harvest".
	// The template uses this to pick a colour for the cycle card.
	Stage string

	// StageLabel is the full human-readable label like "Day 4 (dark)".
	StageLabel string

	// SowDate is formatted like "Mon Jan 02" for display.
	SowDate string

	// HarvestDate is formatted like "Mon Jan 02" for display.
	HarvestDate string

	// HarvestRaw is the harvest date as a time.Time value. The financial
	// enrichment logic uses this to filter cycles by calendar week. The
	// display templates should use HarvestDate (the formatted string) instead.
	HarvestRaw time.Time

	// CostPerTray is the all-in cost to grow one tray (filled by caller).
	CostPerTray float64

	// RevenuePerTray is the total revenue from selling one tray's harvest.
	RevenuePerTray float64

	// ProfitPerTray is revenue minus cost for one tray.
	ProfitPerTray float64

	// HasProfit is true if the crop had enough costing data to calculate.
	HasProfit bool
}

// InventoryItem holds the stock information for one type of tray.
type InventoryItem struct {
	// Name is "Grow trays" or "Bottom trays".
	Name string

	// Total is how many the farm owns.
	Total int

	// InUse is how many are currently out on the farm.
	InUse int

	// Available is Total - InUse.
	Available int
}

// ─────────────────────────────────────────────────────────────────────────────
// Status constants
// ─────────────────────────────────────────────────────────────────────────────

// These constants name the five possible states a cycle can be in relative to
// today's date. Only three of them are ever shown in the snapshot —
// "completed" and "tooFar" are silently excluded.
const (
	statusBlackout  = "blackout"  // trays are in the blackout room right now
	statusLit       = "lit"       // trays are on a lit rack; harvest-day cycles also get this status
	                               // but are shown in "Due for harvest today", not the lit section
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


// printCycleRow writes a single active cycle as a vertical block to w:
//
//	Sunnies 16x Day 4 (dark)
//	sown Sun Mar 08
//	harvest Mon Mar 16
//
// followed by a blank line so consecutive cycles are visually separated.
//
// The "16x" is the tray count; the day label (e.g. "Day 4 (dark)") is
// computed from today's date relative to the cycle's key dates.
//
// The vertical layout wraps cleanly on a phone screen, where the Google
// Calendar description is displayed in a narrow column.
//
// It accepts an io.Writer so the same function works both when printing to
// the terminal and when building a string for Google Calendar.
func printCycleRow(w io.Writer, c farm.Cycle, today, sow, moveToLight, harvest time.Time) {
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

	// Print one line per piece of information so the block wraps cleanly
	// on narrow phone screens. The "Nx" format (e.g. "16x") shows the tray
	// count at a glance without needing the word "trays" to take up space.
	fmt.Fprintf(w, "%s %dx %s\n", task.Capitalize(c.CropName), c.Trays, stageLabel)
	fmt.Fprintf(w, "sown %s\n", sow.Format("Mon Jan 02"))
	fmt.Fprintf(w, "harvest %s\n", harvest.Format("Mon Jan 02"))
	fmt.Fprintln(w) // blank line between consecutive cycle blocks
}

// ─────────────────────────────────────────────────────────────────────────────
// Main display function
// ─────────────────────────────────────────────────────────────────────────────

// writeSnapshot renders the full farm snapshot to any output destination (w).
// Both PrintSnapshot (terminal) and SnapshotText (string for Google Calendar)
// call this function, so the display logic only lives in one place.
func writeSnapshot(w io.Writer, envs []farm.Environment, cycles []farm.Cycle, today time.Time) {
	// Strip the time-of-day from today so all comparisons are date-only.
	// We use time.UTC here because time.Parse (used to read stored date strings)
	// always produces UTC midnight. Using the local timezone for "today" would
	// cause mismatches on machines that are behind UTC — e.g. on a PST machine,
	// midnight PST is 8 AM UTC, which would make today appear "after" midnight
	// UTC on the same calendar date and incorrectly mark cycles as completed.
	t := time.Date(today.Year(), today.Month(), today.Day(), 0, 0, 0, 0, time.UTC)

	// The separator is a short horizontal line that divides sections visually.
	// 21 characters keeps it compact on a phone screen without looking sparse.
	sep := strings.Repeat("─", 21)

	// ── Step 1: classify each cycle ──────────────────────────────────────────
	//
	// Parse the stored date strings into time.Time values once, here, rather
	// than re-parsing them in every helper that needs them. Store the results
	// in a struct alongside the cycle so we can pass everything together.

	type classifiedCycle struct {
		cycle       farm.Cycle
		status      string
		sow         time.Time
		moveToLight time.Time
		harvest     time.Time
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
		// The conflict checker will flag it.
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
	//
	// Short format (e.g. "Wed 03-11") keeps the header compact on small screens.

	fmt.Fprintf(w, "Farm snapshot — %s\n", today.Format("Mon 01-02"))

	// ── Step 4: print each environment section ────────────────────────────────
	//
	// We only show physical environments here (blackout and lit). Inventory
	// rows (grow_trays, bottom_trays) are shown separately in Step 7 — they
	// are not physical spaces that hold trays, so they must be skipped here
	// or they would always appear as "empty".

	for _, env := range envs {
		// Skip inventory rows — they are not physical environments and would
		// always show "empty" because no cycles are ever assigned to them.
		if env.Type == "inventory" {
			continue
		}

		fmt.Fprintln(w, sep)

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

		// Print the environment header, then the cycles (if any).
		if len(envCycles) == 0 {
			fmt.Fprintf(w, "%-22s  empty\n", farm.DisplayName(env.Name))
		} else {
			fmt.Fprintf(w, "%-22s  %d / %d slots\n",
				farm.DisplayName(env.Name), slotsUsed, env.Capacity)
			fmt.Fprintln(w) // blank line before the first cycle block
			for _, cc := range envCycles {
				printCycleRow(w, cc.cycle, t, cc.sow, cc.moveToLight, cc.harvest)
			}
			// printCycleRow already adds a trailing blank line after each block,
			// so no extra blank is needed here before the next separator.
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
		fmt.Fprintln(w, sep)
		fmt.Fprintln(w, "Due for harvest today")
		for _, cc := range harvestToday {
			fmt.Fprintf(w, "%s %dx\n", task.Capitalize(cc.cycle.CropName), cc.cycle.Trays)
			fmt.Fprintf(w, "harvest %s\n", cc.harvest.Format("Mon Jan 02"))
			fmt.Fprintln(w)
		}
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
		fmt.Fprintln(w, sep)
		fmt.Fprintln(w, "Upcoming (next 7 days)")
		for _, cc := range upcoming {
			fmt.Fprintf(w, "%s %dx\n", task.Capitalize(cc.cycle.CropName), cc.cycle.Trays)
			fmt.Fprintf(w, "sow %s\n", cc.sow.Format("Mon Jan 02"))
			fmt.Fprintln(w)
		}
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

		// Load the irrigation mode setting. In "tray_pairs" mode, bottom
		// trays stay paired with grow trays for the entire cycle (sow
		// through harvest). In the default "flood" mode, bottom trays are
		// returned the moment trays move to light.
		cfg, _ := config.Load()
		trayPairs := cfg.IsTrayPairs()

		for _, cc := range classified {
			isHarvestDay := t.Equal(cc.harvest)
			// Grow trays: occupied during blackout and light stages,
			// but free on harvest day (first-thing-in-the-morning rule).
			if (cc.status == statusBlackout || cc.status == statusLit) && !isHarvestDay {
				growInUse += cc.cycle.Trays
			}
			// Bottom trays: in "flood" mode they are only used during
			// blackout (returned at move-to-light). In "tray_pairs" mode
			// they stay paired for the whole cycle, same as grow trays.
			if trayPairs {
				if (cc.status == statusBlackout || cc.status == statusLit) && !isHarvestDay {
					bottomInUse += cc.cycle.Trays
				}
			} else {
				if cc.status == statusBlackout {
					bottomInUse += cc.cycle.Trays
				}
			}
		}

		fmt.Fprintln(w, sep)
		fmt.Fprintln(w, "Tray inventory")
		fmt.Fprintln(w)
		if growTotal > 0 {
			// Each piece of data on its own line so it wraps cleanly on a phone.
			fmt.Fprintln(w, "Grow trays")
			fmt.Fprintf(w, "%d in use / %d total\n", growInUse, growTotal)
			fmt.Fprintf(w, "→ %d available\n", growTotal-growInUse)
			fmt.Fprintln(w)
		}
		if bottomTotal > 0 {
			fmt.Fprintln(w, "Bottom trays")
			fmt.Fprintf(w, "%d in use / %d total\n", bottomInUse, bottomTotal)
			fmt.Fprintf(w, "→ %d available\n", bottomTotal-bottomInUse)
			fmt.Fprintln(w)
		}
	}

	// ── Step 8: conflict checker ──────────────────────────────────────────────
	//
	// Run the conflict checker against all cycles that haven't finished yet.
	// We skip completed cycles (harvest date already in the past) because a
	// conflict that happened three months ago is not useful information today.
	//
	// If any problems are found they appear as a clearly labelled warning
	// section at the bottom of the snapshot, so the grower sees them every
	// time they run "greenies snapshot" until the schedule is fixed.

	// Filter to current and future cycles only (harvest date >= today).
	var activeCycles []farm.Cycle
	for _, c := range cycles {
		harv, _ := time.Parse(task.DateFormat, c.HarvestDate)
		// !harv.Before(t) means harv >= today
		if !harv.Before(t) {
			activeCycles = append(activeCycles, c)
		}
	}

	conflicts := checker.Check(envs, activeCycles)

	if len(conflicts) > 0 {
		fmt.Fprintln(w, sep)
		fmt.Fprintln(w, "CONFLICTS DETECTED")
		fmt.Fprintln(w)
		for _, conflict := range conflicts {
			fmt.Fprintf(w, "  %s\n", conflict)
		}
		fmt.Fprintln(w)
		fmt.Fprintln(w, `  Run "greenies plan" and adjust tray counts or dates to resolve.`)
		fmt.Fprintln(w)
	}
}

// PrintSnapshot prints the full farm snapshot to the terminal.
//
// It shows each environment (blackout room first, then lit environments in the
// order they appear in farm.csv), lists every active cycle currently in that
// environment, and finishes with a "Due for harvest today" reminder and an
// "Upcoming (next 7 days)" section for cycles about to start.
//
// Each cycle is always its own block — two batches of the same crop are two
// separate blocks. Cycles that finished yesterday or earlier do not appear.
func PrintSnapshot(envs []farm.Environment, cycles []farm.Cycle, today time.Time) {
	writeSnapshot(os.Stdout, envs, cycles, today)
}

// SnapshotText returns the full farm snapshot as a plain-English string instead
// of printing it to the terminal.
//
// This is used by "greenies sync" to embed a current farm picture in the
// description of each Google Calendar event — so when the grower taps on any
// farm day in their phone calendar, they can see the full farm state alongside
// the day's task list.
func SnapshotText(envs []farm.Environment, cycles []farm.Cycle, today time.Time) string {
	var sb strings.Builder
	writeSnapshot(&sb, envs, cycles, today)
	return sb.String()
}

// ─────────────────────────────────────────────────────────────────────────────
// Structured snapshot builder (for the GUI)
// ─────────────────────────────────────────────────────────────────────────────

// buildCycleRow converts a classified cycle into a CycleRow struct for the
// GUI template. This is the structured equivalent of printCycleRow() — same
// data, but returned as fields instead of printed as text.
func buildCycleRow(c farm.Cycle, today, sow, moveToLight, harvest time.Time) CycleRow {
	// Day number = days since sow + 1. Day 1 is the sow date.
	dayNum := int(today.Sub(sow).Hours()/24) + 1

	// Work out the stage and build the human-readable label.
	var stage, stageLabel string
	if today.Equal(harvest) {
		stage = "harvest"
		stageLabel = fmt.Sprintf("Day %d (harvest!)", dayNum)
	} else if today.Before(moveToLight) {
		stage = "dark"
		if dayNum == 1 {
			stage = "sow"
			stageLabel = "Day 1 (sow)"
		} else {
			stageLabel = fmt.Sprintf("Day %d (dark)", dayNum)
		}
	} else {
		stage = "light"
		stageLabel = fmt.Sprintf("Day %d (light)", dayNum)
	}

	return CycleRow{
		CropName:    task.Capitalize(c.CropName),
		Trays:       c.Trays,
		DayNum:      dayNum,
		Stage:       stage,
		StageLabel:  stageLabel,
		SowDate:     sow.Format("Mon Jan 02"),
		HarvestDate: harvest.Format("Mon Jan 02"),
		HarvestRaw:  harvest,
	}
}

// BuildSnapshot computes the full farm snapshot and returns it as structured
// data that the GUI can render with styled HTML cards, progress bars, and
// colour-coded stage indicators.
//
// This does the exact same computation as writeSnapshot() — classifying
// cycles, resolving "either" environments, counting slots, running the
// conflict checker — but packs the results into a SnapshotData struct
// instead of printing text.
//
// The CLI continues to use PrintSnapshot() and SnapshotText() for plain
// text output. This function is for the GUI only.
func BuildSnapshot(envs []farm.Environment, cycles []farm.Cycle, today time.Time) SnapshotData {
	// Strip time-of-day so all comparisons are date-only (same as writeSnapshot).
	t := time.Date(today.Year(), today.Month(), today.Day(), 0, 0, 0, 0, time.UTC)

	// ── Step 1: classify each cycle ──────────────────────────────────────
	type classifiedCycle struct {
		cycle       farm.Cycle
		status      string
		sow         time.Time
		moveToLight time.Time
		harvest     time.Time
	}

	var classified []classifiedCycle
	for _, c := range cycles {
		sow, _ := time.Parse(task.DateFormat, c.SowDate)
		mlt, _ := time.Parse(task.DateFormat, c.MoveToLightDate)
		harv, _ := time.Parse(task.DateFormat, c.HarvestDate)

		status := cycleStatus(t, sow, mlt, harv)
		if status == statusCompleted || status == statusTooFar {
			continue
		}
		classified = append(classified, classifiedCycle{c, status, sow, mlt, harv})
	}

	// ── Step 2: resolve "either" lit environments ────────────────────────
	litUsage := map[string]int{}
	for _, cc := range classified {
		if cc.status == statusLit && cc.cycle.LitEnvironment != "either" {
			litUsage[cc.cycle.LitEnvironment] += cc.cycle.Trays
		}
	}

	var litEnvs []farm.Environment
	for _, e := range envs {
		if e.Type == "lit" {
			litEnvs = append(litEnvs, e)
		}
	}

	resolvedEnv := map[string]string{}
	for _, cc := range classified {
		if cc.status != statusLit || cc.cycle.LitEnvironment != "either" {
			continue
		}
		assigned := ""
		for _, e := range litEnvs {
			if litUsage[e.Name]+cc.cycle.Trays <= e.Capacity {
				assigned = e.Name
				break
			}
		}
		if assigned == "" && len(litEnvs) > 0 {
			assigned = litEnvs[0].Name
		}
		resolvedEnv[cc.cycle.CycleID] = assigned
		litUsage[assigned] += cc.cycle.Trays
	}

	effectiveEnv := func(cc classifiedCycle) string {
		if cc.cycle.LitEnvironment == "either" {
			return resolvedEnv[cc.cycle.CycleID]
		}
		return cc.cycle.LitEnvironment
	}

	// ── Step 3: build environment sections ───────────────────────────────
	var sections []EnvSection
	for _, env := range envs {
		if env.Type == "inventory" {
			continue
		}

		section := EnvSection{
			Name:     farm.DisplayName(env.Name),
			Type:     env.Type,
			Capacity: env.Capacity,
		}

		// Find cycles belonging to this environment.
		if env.Type == "blackout" {
			for _, cc := range classified {
				if cc.status == statusBlackout {
					section.Cycles = append(section.Cycles, buildCycleRow(cc.cycle, t, cc.sow, cc.moveToLight, cc.harvest))
					section.SlotsUsed += cc.cycle.Trays
				}
			}
		} else {
			for _, cc := range classified {
				if cc.status == statusLit && effectiveEnv(cc) == env.Name && !t.Equal(cc.harvest) {
					section.Cycles = append(section.Cycles, buildCycleRow(cc.cycle, t, cc.sow, cc.moveToLight, cc.harvest))
					section.SlotsUsed += cc.cycle.Trays
				}
			}
		}

		sections = append(sections, section)
	}

	// ── Step 4: harvest today ────────────────────────────────────────────
	var harvestToday []CycleRow
	for _, cc := range classified {
		if t.Equal(cc.harvest) {
			harvestToday = append(harvestToday, buildCycleRow(cc.cycle, t, cc.sow, cc.moveToLight, cc.harvest))
		}
	}

	// ── Step 5: upcoming (next 7 days) ──────────────────────────────────
	var upcoming []CycleRow
	for _, cc := range classified {
		if cc.status == statusUpcoming {
			upcoming = append(upcoming, buildCycleRow(cc.cycle, t, cc.sow, cc.moveToLight, cc.harvest))
		}
	}

	// ── Step 6: tray inventory ───────────────────────────────────────────
	var inventory []InventoryItem
	growTotal, bottomTotal := 0, 0
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
		growInUse, bottomInUse := 0, 0

		// Load irrigation mode — "tray_pairs" means bottom trays stay
		// paired for the whole cycle, "flood" (default) means they are
		// returned at move-to-light.
		cfg, _ := config.Load()
		trayPairs := cfg.IsTrayPairs()

		for _, cc := range classified {
			isHarvestDay := t.Equal(cc.harvest)
			if (cc.status == statusBlackout || cc.status == statusLit) && !isHarvestDay {
				growInUse += cc.cycle.Trays
			}
			if trayPairs {
				if (cc.status == statusBlackout || cc.status == statusLit) && !isHarvestDay {
					bottomInUse += cc.cycle.Trays
				}
			} else {
				if cc.status == statusBlackout {
					bottomInUse += cc.cycle.Trays
				}
			}
		}
		if growTotal > 0 {
			inventory = append(inventory, InventoryItem{
				Name:      "Grow trays",
				Total:     growTotal,
				InUse:     growInUse,
				Available: growTotal - growInUse,
			})
		}
		if bottomTotal > 0 {
			inventory = append(inventory, InventoryItem{
				Name:      "Bottom trays",
				Total:     bottomTotal,
				InUse:     bottomInUse,
				Available: bottomTotal - bottomInUse,
			})
		}
	}

	// ── Step 7: conflict checker ─────────────────────────────────────────
	var activeCycles []farm.Cycle
	for _, c := range cycles {
		harv, _ := time.Parse(task.DateFormat, c.HarvestDate)
		if !harv.Before(t) {
			activeCycles = append(activeCycles, c)
		}
	}
	conflicts := checker.Check(envs, activeCycles)

	// ── Assemble and return ──────────────────────────────────────────────
	return SnapshotData{
		DateLabel:    today.Format("Mon 01-02"),
		Environments: sections,
		HarvestToday: harvestToday,
		Upcoming:     upcoming,
		Inventory:    inventory,
		Conflicts:    conflicts,
		HasData:      len(classified) > 0 || len(sections) > 0,
	}
}

// CalendarTitle returns a compact slot-usage summary for use as a Google
// Calendar event title. Example: "Farm 48/80 Blackout 16/100"
//
// "Farm" combines all lit environments (main tent + test tent) into a single
// number — it represents the total lit rack capacity on the farm. Each lit
// environment is separate in the full snapshot, but on a phone calendar title
// there is only room for one combined figure.
//
// The blackout room is shown separately because it operates independently of
// the lit environments — a grower needs to see both numbers to know at a
// glance whether they are tight on blackout space, lit space, or both.
func CalendarTitle(envs []farm.Environment, cycles []farm.Cycle, today time.Time) string {
	t := time.Date(today.Year(), today.Month(), today.Day(), 0, 0, 0, 0, time.UTC)

	// Sum up capacities from the environment config.
	blackoutCapacity := 0
	litCapacity := 0
	for _, e := range envs {
		switch e.Type {
		case "blackout":
			blackoutCapacity += e.Capacity
		case "lit":
			litCapacity += e.Capacity
		}
	}

	// Count how many slots each active cycle is currently occupying.
	blackoutInUse := 0
	litInUse := 0
	for _, c := range cycles {
		sow, _ := time.Parse(task.DateFormat, c.SowDate)
		mlt, _ := time.Parse(task.DateFormat, c.MoveToLightDate)
		harv, _ := time.Parse(task.DateFormat, c.HarvestDate)

		status := cycleStatus(t, sow, mlt, harv)

		switch status {
		case statusBlackout:
			blackoutInUse += c.Trays
		case statusLit:
			// Harvest-day trays are cut first thing in the morning,
			// so those slots are considered free for the title.
			if !t.Equal(harv) {
				litInUse += c.Trays
			}
		}
	}

	return fmt.Sprintf("Farm %d/%d Blackout %d/%d",
		litInUse, litCapacity, blackoutInUse, blackoutCapacity)
}
