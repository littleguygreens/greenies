// Package checker scans planned crop cycles against the farm's physical limits
// and reports any capacity problems it finds.
//
// Think of this as the programme's "does it fit?" engine. Before you commit to
// a planting plan it can tell you things like:
//
//   - "Main tent will have 70 trays on 10 Mar but only has 64 slots — 6 over"
//   - "You'll have 155 grow trays in use on 12 Mar but only own 150"
//
// It also runs as a background check inside "greenies snapshot" so you always
// know whether your current schedule has any problems.
//
// The checker never writes anything — it is purely a read-and-report tool.
package checker

import (
	"fmt"
	"sort"
	"strings"
	"time"

	"github.com/littleguygreens/greenies/internal/farm"
	"github.com/littleguygreens/greenies/internal/task"
)

// Check scans all planned cycles against the farm's physical limits and returns
// a list of human-readable warning strings — one per capacity problem found.
//
// It checks three resources:
//
//  1. Lit slot capacity — does any lit environment (e.g. main tent) have more
//     trays on its racks than it has physical shelf slots?
//
//  2. Grow tray stock — does the farm have more grow trays out on the farm than
//     it actually owns?
//
//  3. Bottom tray stock — same check for bottom trays (which are only in use
//     during the blackout stage).
//
// Rather than checking every single calendar day (which could be slow for
// long-range plans), Check only inspects the "change point" dates — the dates
// when resource usage actually changes: sow dates, move-to-light dates, and
// harvest dates. Conflicts at intermediate days are always caught because usage
// only changes at these boundary events.
//
// If no conflicts are found, Check returns nil (an empty list).
func Check(envs []farm.Environment, cycles []farm.Cycle) []string {
	if len(cycles) == 0 {
		return nil
	}

	// ── Step 1: collect capacity limits from the farm layout ──────────────────

	// litCap maps environment name → total slot count.
	litCap := map[string]int{}

	// growTotal and bottomTotal are the number of each tray type the farm owns.
	// If either is 0 it means there is no inventory row in farm.csv, and we
	// skip that check rather than reporting a false "over 0" conflict.
	growTotal := 0
	bottomTotal := 0

	// Also collect the lit environments in config order — needed to resolve
	// "either" cycles (cycles where the grower hadn't picked a tent yet).
	var litEnvList []farm.Environment

	for _, e := range envs {
		switch e.Type {
		case "lit":
			litCap[e.Name] = e.Capacity
			litEnvList = append(litEnvList, e)
		case "inventory":
			switch e.Name {
			case "grow_trays":
				growTotal = e.Capacity
			case "bottom_trays":
				bottomTotal = e.Capacity
			}
		}
	}

	// ── Step 2: parse all cycle dates and resolve "either" environments ───────

	// parsedCycle holds a cycle with its dates already converted from strings
	// into time.Time values (so we don't re-parse on every date we check), and
	// with "either" replaced by a real environment name.
	type parsedCycle struct {
		c           farm.Cycle
		sow         time.Time
		moveToLight time.Time
		harvest     time.Time
		litEnv      string // always a real env name — never "either"
	}

	// To resolve "either" cycles we use the same cascading-assignment logic as
	// the snapshot: assign to the first lit env that has room, falling through
	// to the next if that one is full, and to the first env (flagging it as a
	// conflict) if all are full.
	//
	// This resolution is "snapshot-style" (based on total tray counts across
	// the whole schedule, not day-by-day). It is a good approximation and
	// matches what the user sees in the snapshot display.
	litUsageForResolution := map[string]int{}
	for _, c := range cycles {
		// Pre-count trays for non-"either" cycles so "either" assignments
		// can cascade around them.
		if c.LitEnvironment != "either" {
			litUsageForResolution[c.LitEnvironment] += c.Trays
		}
	}

	var parsed []parsedCycle

	// dateSet collects every "change point" date across all cycles.
	// Using a map deduplicates dates that appear in multiple cycles.
	dateSet := map[time.Time]bool{}

	for _, c := range cycles {
		// time.Parse is safe to ignore here — dates were written by the program
		// and are always valid YYYY-MM-DD strings.
		sow, _ := time.Parse(task.DateFormat, c.SowDate)
		mlt, _ := time.Parse(task.DateFormat, c.MoveToLightDate)
		harv, _ := time.Parse(task.DateFormat, c.HarvestDate)

		// Resolve the lit environment for this cycle.
		env := c.LitEnvironment
		if env == "either" {
			// Try each lit env in config order; pick the first with room.
			assigned := ""
			for _, e := range litEnvList {
				if litUsageForResolution[e.Name]+c.Trays <= e.Capacity {
					assigned = e.Name
					break
				}
			}
			// If no env has room, assign to the first one — a conflict will
			// be reported when we check that date.
			if assigned == "" && len(litEnvList) > 0 {
				assigned = litEnvList[0].Name
			}
			env = assigned
			litUsageForResolution[env] += c.Trays
		}

		parsed = append(parsed, parsedCycle{c, sow, mlt, harv, env})

		// The dates when resource usage changes for this cycle:
		//   sow          → grow trays and bottom trays are added to the farm
		//   moveToLight  → bottom trays are returned; lit slots are occupied
		//   harvest-1    → the last day lit slots and grow trays are in use
		//                  (on harvest day itself they are free — grower cuts
		//                   first thing in the morning)
		//
		// We check harvest-1 instead of harvest so we catch the peak usage
		// on the day before slots are released.
		dateSet[sow] = true
		dateSet[mlt] = true
		if prev := harv.AddDate(0, 0, -1); !prev.Before(sow) {
			// Only add harvest-1 if the cycle is at least 2 days long —
			// a 1-day cycle would give a date before sow, which is nonsense.
			dateSet[prev] = true
		}
	}

	// Sort the change-point dates into chronological order.
	var dates []time.Time
	for d := range dateSet {
		dates = append(dates, d)
	}
	sort.Slice(dates, func(i, j int) bool {
		return dates[i].Before(dates[j])
	})

	// ── Step 3: check each change-point date for capacity violations ──────────

	// We report each type of conflict only once (on the first date it occurs)
	// to avoid flooding the user with identical warnings for every day of a
	// multi-day overrun.
	//
	// conflictKey uniquely identifies one type of conflict:
	//   - resource is "lit", "grow", or "bottom"
	//   - env is the environment name for lit conflicts (empty for tray stock)
	type conflictKey struct{ resource, env string }
	reported := map[conflictKey]bool{}

	var warnings []string

	for _, date := range dates {
		// Compute resource usage on this specific date for every cycle.
		litUsage := map[string]int{}       // env name → trays in use
		litNames := map[string][]string{}  // env name → contributing cycle labels

		growInUse := 0
		var growNames []string

		bottomInUse := 0
		var bottomNames []string

		for _, pc := range parsed {
			// A short human-readable label for this cycle, used in conflict
			// messages so the grower knows exactly which batches are clashing.
			// Example: "Sunnies (sown Mar 03, 2 trays)"
			label := fmt.Sprintf("%s (sown %s, %d %s)",
				task.Capitalize(pc.c.CropName), pc.sow.Format("Jan 02"), pc.c.Trays, task.TrayWord(pc.c.Trays))

			// Grow trays are in use from the sow date up to (but not including)
			// the harvest date. On harvest day the grower cuts first thing in
			// the morning, so those trays are immediately free.
			// Date range: sow <= date < harvest
			if !date.Before(pc.sow) && date.Before(pc.harvest) {
				growInUse += pc.c.Trays
				growNames = append(growNames, label)
			}

			// Bottom trays are in use from the sow date up to (but not
			// including) the move-to-light date. They are returned to the
			// cupboard the moment trays go onto the lit rack.
			// Date range: sow <= date < moveToLight
			if !date.Before(pc.sow) && date.Before(pc.moveToLight) {
				bottomInUse += pc.c.Trays
				bottomNames = append(bottomNames, label)
			}

			// Lit slots are occupied from the move-to-light date up to (but
			// not including) the harvest date.
			// Date range: moveToLight <= date < harvest
			if !date.Before(pc.moveToLight) && date.Before(pc.harvest) {
				litUsage[pc.litEnv] += pc.c.Trays
				litNames[pc.litEnv] = append(litNames[pc.litEnv], label)
			}
		}

		// Format the date for display in warning messages.
		// "Jan 02" gives a short friendly label like "Mar 10".
		dateLabel := date.Format("Jan 02")

		// Check each lit environment for slot overruns.
		for env, used := range litUsage {
			cap, ok := litCap[env]
			if !ok {
				// This env name doesn't match anything in farm.csv — skip.
				continue
			}
			if used <= cap {
				continue // no overrun
			}
			key := conflictKey{"lit", env}
			if reported[key] {
				continue // already reported this conflict
			}
			reported[key] = true

			warnings = append(warnings, fmt.Sprintf(
				"%s: %d/%d slots in use on %s (%d over) — %s",
				farm.DisplayName(env), used, cap, dateLabel, used-cap,
				strings.Join(litNames[env], ", ")))
		}

		// Check grow tray stock.
		if growTotal > 0 && growInUse > growTotal {
			key := conflictKey{"grow", ""}
			if !reported[key] {
				reported[key] = true
				warnings = append(warnings, fmt.Sprintf(
					"Grow tray stock: %d/%d in use on %s (%d over) — %s",
					growInUse, growTotal, dateLabel, growInUse-growTotal,
					strings.Join(growNames, ", ")))
			}
		}

		// Check bottom tray stock.
		if bottomTotal > 0 && bottomInUse > bottomTotal {
			key := conflictKey{"bottom", ""}
			if !reported[key] {
				reported[key] = true
				warnings = append(warnings, fmt.Sprintf(
					"Bottom tray stock: %d/%d in use on %s (%d over) — %s",
					bottomInUse, bottomTotal, dateLabel, bottomInUse-bottomTotal,
					strings.Join(bottomNames, ", ")))
			}
		}
	}

	return warnings
}

