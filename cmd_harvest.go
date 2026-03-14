package main

import (
	"bufio"   // for reading a full line of user input from the terminal
	"fmt"
	"os"      // for os.Exit
	"sort"    // for sorting the harvest log by date
	"strconv" // for converting text like "2" into the number 2
	"strings" // for string utilities used throughout
	"time"

	"github.com/littleguygreens/greenies/internal/farm"
	"github.com/littleguygreens/greenies/internal/task"
)

// runHarvest handles the "greenies harvest" command.
//
// It shows all crop cycles whose harvest date is within the last 30 days and
// that have not yet been logged, lets the grower pick one, and records the
// actual tray count and gram weight alongside the planned expectations.
//
// Each logged batch gets its own record — a day where two crops were both
// harvested would produce two separate records, one per cycle.
func runHarvest() {
	// Load cycles and the existing harvest log.
	cycles, err := farm.LoadCycles()
	if err != nil {
		fmt.Printf("Error loading cycle records: %v\n", err)
		os.Exit(1)
	}

	harvests, err := farm.LoadHarvests()
	if err != nil {
		fmt.Printf("Error loading harvest log: %v\n", err)
		os.Exit(1)
	}

	// Build a set of CycleIDs that have already been logged, so we don't
	// offer the same batch twice.
	logged := map[string]bool{}
	for _, h := range harvests {
		logged[h.CycleID] = true
	}

	// task.Today() returns midnight UTC on the current local date. See task/task.go.
	today := task.Today()

	// The log window: any cycle harvested in the last 30 days.
	// 30 days gives the grower plenty of time to log without nagging, but
	// keeps the list short — batches older than a month fall off the list.
	cutoff := today.AddDate(0, 0, -30)

	// Collect cycles eligible for logging: harvest date is past (or today)
	// and within the 30-day window, and not yet in the log.
	var eligible []farm.Cycle
	for _, c := range cycles {
		harv, _ := time.Parse(task.DateFormat, c.HarvestDate)
		// harv <= today  →  !today.Before(harv)
		// harv >= cutoff →  !harv.Before(cutoff)
		if !today.Before(harv) && !harv.Before(cutoff) && !logged[c.CycleID] {
			eligible = append(eligible, c)
		}
	}

	if len(eligible) == 0 {
		fmt.Println("No recent harvests to log.")
		fmt.Println("Cycles are eligible to log for 30 days after their harvest date.")
		fmt.Println("Use \"greenies harvestlog\" to see previously logged harvests.")
		return
	}

	// Show the list sorted by harvest date, most recent first.
	sort.Slice(eligible, func(i, j int) bool {
		return eligible[i].HarvestDate > eligible[j].HarvestDate
	})

	fmt.Println("Recent harvests ready to log:")
	fmt.Println()
	for i, c := range eligible {
		harv, _ := time.Parse(task.DateFormat, c.HarvestDate)
		trayWord := "tray"
		if c.Trays != 1 {
			trayWord = "trays"
		}
		// Show the expected grams if we have them; skip if unknown (older cycles).
		expectedLabel := ""
		if c.ExpectedGrams > 0 {
			expectedLabel = fmt.Sprintf("   expected: %dg", c.ExpectedGrams)
		}
		fmt.Printf("  %d.  %-12s  %d %s   harvest %s%s\n",
			i+1, task.Capitalize(c.CropName), c.Trays, trayWord,
			harv.Format("Jan 02"), expectedLabel)
	}
	fmt.Println()
	fmt.Println("  (Type \"cancel\" at any prompt to exit without saving.)")
	fmt.Println()

	// Set up the ask helper with cancel support — same pattern as runPlan().
	reader := bufio.NewReader(os.Stdin)
	ask := func(prompt string) string {
		fmt.Print(prompt)
		line, _ := reader.ReadString('\n')
		v := strings.TrimSpace(line)
		if strings.EqualFold(v, "cancel") {
			fmt.Println("Cancelled.")
			os.Exit(0)
		}
		return v
	}

	// Ask which cycle to log.
	choiceStr := ask(fmt.Sprintf("Which cycle to log? (1-%d): ", len(eligible)))
	n, err := strconv.Atoi(choiceStr)
	if err != nil || n < 1 || n > len(eligible) {
		fmt.Printf("Please enter a number between 1 and %d.\n", len(eligible))
		os.Exit(1)
	}
	chosen := eligible[n-1]

	harv, _ := time.Parse(task.DateFormat, chosen.HarvestDate)
	chosenTrayWord := "tray"
	if chosen.Trays != 1 {
		chosenTrayWord = "trays"
	}
	fmt.Printf("\nLogging harvest: %s — %d %s — harvest %s\n\n",
		task.Capitalize(chosen.CropName), chosen.Trays, chosenTrayWord,
		harv.Format("Jan 02"))

	// Ask actual trays. The default is the planned tray count — press Enter
	// to accept it. The grower only needs to type a different number if they
	// lost a tray (e.g. mould, accident).
	actualTraysStr := ask(fmt.Sprintf("Actual trays harvested [%d]: ", chosen.Trays))
	actualTrays := chosen.Trays // default
	if actualTraysStr != "" {
		t, err := strconv.Atoi(actualTraysStr)
		if err != nil || t < 0 {
			fmt.Println("Please enter a whole number (e.g. 3). Type 0 if no usable crop was cut.")
			os.Exit(1)
		}
		actualTrays = t
	}

	// Ask actual grams. The default is the expected yield if we have it,
	// or blank (must type a number) if the cycle pre-dates the ExpectedGrams field.
	var gramsPrompt string
	defaultGrams := chosen.ExpectedGrams
	if defaultGrams > 0 {
		gramsPrompt = fmt.Sprintf("Actual grams harvested [%d]: ", defaultGrams)
	} else {
		gramsPrompt = "Actual grams harvested: "
	}
	actualGramsStr := ask(gramsPrompt)

	// Parse actual grams.
	actualGrams := defaultGrams // default (may be 0 if unknown)
	if actualGramsStr != "" {
		g, err := strconv.Atoi(actualGramsStr)
		if err != nil || g < 0 {
			fmt.Println("Please enter a whole number in grams (e.g. 1400).")
			os.Exit(1)
		}
		actualGrams = g
	} else if defaultGrams == 0 {
		// No default and user pressed Enter — we need a number.
		fmt.Println("Please enter the actual grams harvested (e.g. 1400).")
		os.Exit(1)
	}

	// Optional notes — pressing Enter skips this field.
	notes := ask("Notes (optional — press Enter to skip): ")

	// Build the record and save.
	record := farm.HarvestRecord{
		CycleID:       chosen.CycleID,
		CropName:      chosen.CropName,
		HarvestDate:   chosen.HarvestDate,
		ExpectedTrays: chosen.Trays,
		ActualTrays:   actualTrays,
		ExpectedGrams: chosen.ExpectedGrams,
		ActualGrams:   actualGrams,
		Notes:         notes,
	}

	harvests = append(harvests, record)
	if err := farm.SaveHarvests(harvests); err != nil {
		fmt.Printf("Error saving harvest record: %v\n", err)
		os.Exit(1)
	}

	fmt.Printf("\nHarvest logged — %s, %d trays, %dg.\n",
		task.Capitalize(chosen.CropName), actualTrays, actualGrams)
	fmt.Println("Run \"greenies harvestlog\" to see your full history.")
}

// runHarvestLog handles the "greenies harvestlog" command.
//
// It prints all saved harvest records sorted most-recent-first, showing the
// planned yield alongside what was actually cut — so the grower can spot
// trends over time (e.g. a crop that consistently under-yields).
func runHarvestLog() {
	harvests, err := farm.LoadHarvests()
	if err != nil {
		fmt.Printf("Error loading harvest log: %v\n", err)
		os.Exit(1)
	}

	if len(harvests) == 0 {
		fmt.Println("No harvests logged yet.")
		fmt.Println("Run \"greenies harvest\" after each harvest to build your log.")
		return
	}

	// Sort most recent first. HarvestDate is YYYY-MM-DD, so string comparison
	// gives the same result as date comparison — later dates sort higher.
	sort.Slice(harvests, func(i, j int) bool {
		return harvests[i].HarvestDate > harvests[j].HarvestDate
	})

	fmt.Printf("Harvest log — %d records\n", len(harvests))
	fmt.Println(strings.Repeat("─", 70))
	fmt.Println()

	for _, h := range harvests {
		harv, _ := time.Parse(task.DateFormat, h.HarvestDate)

		// Tray column: "3/3 trays" or "2/3 trays" (actual/expected).
		// This immediately shows whether any trays were lost.
		trayCol := fmt.Sprintf("%d/%d trays", h.ActualTrays, h.ExpectedTrays)

		// Gram column: show actual alongside expected (if we have expected data).
		// Old cycles logged before ExpectedGrams was added will show just actual.
		var gramCol string
		if h.ExpectedGrams > 0 {
			gramCol = fmt.Sprintf("%dg / %dg expected", h.ActualGrams, h.ExpectedGrams)
		} else {
			gramCol = fmt.Sprintf("%dg", h.ActualGrams)
		}

		fmt.Printf("  %s   %-12s  %-10s  %s\n",
			harv.Format("Jan 02"),
			task.Capitalize(h.CropName),
			trayCol,
			gramCol)

		// Notes appear on their own line, indented to align under the crop name.
		if h.Notes != "" {
			fmt.Printf("               %s\n", h.Notes)
		}
	}

	fmt.Println()
}
