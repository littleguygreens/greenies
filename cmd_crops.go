package main

import (
	"bufio"   // for reading a full line of user input from the terminal
	"context" // for context.Background(), used when pushing to Google Sheets
	"fmt"
	"os"      // for os.Exit
	"strconv" // for converting text like "2" into the number 2
	"strings" // for strings.Repeat and other string utilities

	"github.com/littleguygreens/greenies/internal/config"
	"github.com/littleguygreens/greenies/internal/crop"
	"github.com/littleguygreens/greenies/internal/gcal"
)

// runCrops handles the "greenies crops" command.
//
// It reads the crop library and prints a summary of every available variety,
// then offers an option to add a new crop interactively. Adding a crop walks
// the grower through every parameter and then asks for stage + tasks for each
// day of the cycle — the same workflow as the GUI "Add New Crop" page.
func runCrops() {
	reader := bufio.NewReader(os.Stdin)

	// ask is a local helper that prints a prompt, reads a line of input, and
	// handles the "cancel" escape hatch — typing "cancel" at any prompt exits
	// cleanly without saving anything.
	ask := func(prompt string) string {
		fmt.Print(prompt)
		text, _ := reader.ReadString('\n')
		text = strings.TrimSpace(text)
		if strings.ToLower(text) == "cancel" {
			fmt.Println("Cancelled — nothing was saved.")
			os.Exit(0)
		}
		return text
	}

	// askInt reads a number from the terminal. If the input is empty, it
	// returns the default value. If it is not a valid number, it asks again.
	askInt := func(prompt string, defaultVal int) int {
		for {
			raw := ask(prompt)
			if raw == "" {
				return defaultVal
			}
			n, err := strconv.Atoi(raw)
			if err != nil {
				fmt.Println("  Please enter a whole number (or press Enter for the default).")
				continue
			}
			return n
		}
	}

	// askFloat reads a decimal number from the terminal. Works the same as
	// askInt but allows values like "1.5".
	askFloat := func(prompt string, defaultVal float64) float64 {
		for {
			raw := ask(prompt)
			if raw == "" {
				return defaultVal
			}
			f, err := strconv.ParseFloat(raw, 64)
			if err != nil {
				fmt.Println("  Please enter a number (or press Enter for the default).")
				continue
			}
			return f
		}
	}

	// ── Show the crop library ───────────────────────────────────────────

	// Load the crop library using the shared factory function.
	source, err := crop.GetSource()
	if err != nil {
		fmt.Printf("Error: %v\n", err)
		os.Exit(1)
	}

	crops, err := source.LoadCrops()
	if err != nil {
		fmt.Printf("Error loading crop library: %v\n", err)
		os.Exit(1)
	}

	fmt.Printf("Crop library (%d varieties)\n", len(crops))
	fmt.Println(strings.Repeat("─", 60))

	for i, c := range crops {
		// Show a numbered list so the grower can pick one to edit.
		fmt.Printf("  %d. %-12s  %d days   seed: %dg/tray   yield: %dg/tray\n",
			i+1, c.Name, c.CycleDays, c.SeedGrams, c.YieldGrams)
	}
	fmt.Println()

	// ── Offer to add or edit a crop ─────────────────────────────────────

	choice := strings.ToLower(ask("(a)dd new crop, (e)dit existing  [Enter to exit]: "))

	if choice == "e" {
		// ── Edit an existing crop ───────────────────────────────────────
		editCropCLI(crops, reader, ask, askInt, askFloat)
		return
	}

	if choice != "a" {
		return
	}

	fmt.Println()
	fmt.Println("Let's add a new crop to the library.")
	fmt.Println("Type \"cancel\" at any prompt to exit without saving.")
	fmt.Println()

	// ── Crop name ───────────────────────────────────────────────────────

	var cropName string
	for {
		cropName = strings.ToLower(ask("Crop name: "))
		if cropName == "" {
			fmt.Println("  Name cannot be empty.")
			continue
		}
		break
	}

	// ── Soak type ───────────────────────────────────────────────────────
	//
	// Some crops need their seeds soaked before sowing. There are three
	// options:
	//   (n)one      — no soak at all (e.g. broccoli)
	//   (s)ame-day  — soak for a few hours on sowing day (e.g. sunnies, 4h)
	//   (o)vernight — soak overnight, seeds go onto trays the next morning
	//                 (e.g. peas). This adds a Day 0 to the cycle.

	overnightSoak := false
	soakHours := 0

	soakChoice := strings.ToLower(ask("Soak type — (n)one, (s)ame-day hours, (o)vernight [n]: "))
	switch soakChoice {
	case "s":
		soakHours = askInt("  Soak hours (e.g. 4): ", 4)
	case "o":
		overnightSoak = true
		soakHours = 12 // sensible default for overnight soaks
	}

	// ── Numeric parameters ──────────────────────────────────────────────

	seedGrams := askInt("Seed grams per tray (e.g. 150) [0]: ", 0)
	dirtLitres := askFloat("Dirt litres per tray [1]: ", 1.0)
	yieldGrams := askInt("Expected yield grams per tray (e.g. 500) [0]: ", 0)

	// ── Costing parameters (optional) ───────────────────────────────────
	//
	// These let the grower enter what they pay for seed and what they
	// charge per unit. All optional — press Enter to skip.

	fmt.Println()
	fmt.Println("Costing (optional — press Enter to skip):")
	seedCost := askFloat("  Seed bag cost in $ (e.g. 15.00) [0]: ", 0)
	seedPurchaseWeight := askInt("  Seed bag weight (e.g. 500) [0]: ", 0)
	// Ask what unit the weight is in — convert kg to grams for storage.
	if seedPurchaseWeight > 0 {
		unit := strings.ToLower(ask("  Weight unit — (g)rams or (k)g [g]: "))
		if unit == "k" || unit == "kg" {
			seedPurchaseWeight *= 1000
		}
	}
	unitWeight := askInt("  Grams per sellable unit (e.g. 100) [100]: ", 100)
	unitSellPrice := askFloat("  Sell price per unit in $ (e.g. 4.50) [0]: ", 0)
	fmt.Println()

	// ── Total cycle days ────────────────────────────────────────────────
	//
	// This is how many days the crop takes from sow to harvest. For crops
	// with an overnight soak, the Day 0 soak day is included in the cycle
	// but the "total days" number refers to the last day number (e.g. peas
	// are an "8 day" crop with Day 0 through Day 8).

	var totalDays int
	for {
		totalDays = askInt("Total cycle days (sow to harvest, e.g. 9): ", 0)
		if totalDays < 2 {
			fmt.Println("  A crop needs at least 2 days (sow and harvest).")
			continue
		}
		break
	}

	// ── Day-by-day tasks ────────────────────────────────────────────────
	//
	// Now the grower enters what happens on each day of the cycle. For each
	// day they pick a stage (sow/dark/light/harvest) and type the tasks.
	//
	// The valid stages are:
	//   sow     — seed preparation and tray setup
	//   dark    — trays are in the blackout room
	//   light   — trays are on lit racks
	//   harvest — cut and wash day

	fmt.Println()
	fmt.Println("Now enter the stage and tasks for each day of the cycle.")
	fmt.Println("Stages: (s)ow, (d)ark, (l)ight, (h)arvest")
	fmt.Println("Tasks: type a comma-separated list, or press Enter for none.")
	fmt.Println()

	// Work out which day number to start from. Overnight soak crops start
	// at Day 0 (the soak day), everything else starts at Day 1.
	startDay := 1
	if overnightSoak {
		startDay = 0
	}

	var days []crop.CropDay
	darkDays := 0
	lightDays := 0

	for day := startDay; day <= totalDays; day++ {
		// Show the day number with a default stage suggestion to speed
		// things up. The defaults follow the typical pattern:
		//   Day 0 or 1 → sow
		//   Last day → harvest
		//   Everything else → dark for the first half, light for the second
		defaultStage := "dark"
		defaultLetter := "d"
		if day == startDay || (overnightSoak && day <= 1) {
			defaultStage = "sow"
			defaultLetter = "s"
		} else if day == totalDays {
			defaultStage = "harvest"
			defaultLetter = "h"
		} else {
			// Split the middle days roughly in half between dark and light.
			sowDays := 1
			if overnightSoak {
				sowDays = 2
			}
			middleDays := totalDays - sowDays - 1 // minus harvest day
			halfPoint := sowDays + (middleDays+1)/2
			if day > halfPoint {
				defaultStage = "light"
				defaultLetter = "l"
			}
		}

		// Ask for the stage. The grower can just press Enter to accept the
		// default, or type the first letter of the stage name.
		stageInput := strings.ToLower(ask(fmt.Sprintf("  Day %d — stage [%s]: ", day, defaultLetter)))
		stage := defaultStage
		switch stageInput {
		case "s", "sow":
			stage = "sow"
		case "d", "dark":
			stage = "dark"
		case "l", "light":
			stage = "light"
		case "h", "harvest":
			stage = "harvest"
		case "":
			// Keep the default
		default:
			fmt.Printf("    Unknown stage %q — using %q\n", stageInput, defaultStage)
		}

		// Ask for tasks. The grower types a free-form description of what
		// needs doing on this day. Empty is fine — some days have no tasks
		// (e.g. automated watering on lit racks).
		tasks := ask(fmt.Sprintf("  Day %d — tasks: ", day))

		days = append(days, crop.CropDay{
			Day:   day,
			Stage: stage,
			Tasks: tasks,
		})

		switch stage {
		case "dark":
			darkDays++
		case "light":
			lightDays++
		}
	}

	// ── Build the Crop and save ─────────────────────────────────────────

	newCrop := crop.Crop{
		Name:               cropName,
		CycleDays:          days[len(days)-1].Day,
		OvernightSoak:      overnightSoak,
		SoakHours:          soakHours,
		SeedGrams:          seedGrams,
		DirtLitres:         dirtLitres,
		DarkDays:           darkDays,
		LightDays:          lightDays,
		YieldGrams:         yieldGrams,
		SeedCost:           seedCost,
		SeedPurchaseWeight: seedPurchaseWeight,
		UnitWeight:         unitWeight,
		UnitSellPrice:      unitSellPrice,
		Days:               days,
	}

	if err := crop.AppendCrop(newCrop); err != nil {
		fmt.Printf("\nError: %v\n", err)
		os.Exit(1)
	}

	fmt.Printf("\nAdded \"%s\" to your crop library.\n", cropName)

	// Sync to Google Sheets if enabled (fire-and-forget — don't block the
	// terminal waiting for a network request).
	cfg, _ := config.Load()
	if cfg.SheetsEnabled && cfg.SheetID != "" {
		fmt.Println("Syncing to Google Sheets...")
		gcal.SyncLocalToSheet(context.Background())
	}

	fmt.Println("Run \"greenies plan\" to schedule a cycle of this crop.")
}

// editCropCLI walks the grower through editing an existing crop. It uses the
// same prompt style as adding a crop, but every question shows the current
// value in square brackets — pressing Enter keeps the current value.
//
// Parameters:
//   - crops: the full list of crops (already loaded by runCrops)
//   - reader: the terminal input reader (shared with runCrops)
//   - ask, askInt, askFloat: the same prompt helpers used by runCrops
func editCropCLI(
	crops []crop.Crop,
	reader *bufio.Reader,
	ask func(string) string,
	askInt func(string, int) int,
	askFloat func(string, float64) float64,
) {
	fmt.Println()
	fmt.Println("Which crop do you want to edit? Enter the number from the list above.")
	fmt.Println("Type \"cancel\" at any prompt to exit without saving.")
	fmt.Println()

	// ── Pick a crop by number ───────────────────────────────────────────

	var picked crop.Crop
	for {
		raw := ask(fmt.Sprintf("Crop number (1-%d): ", len(crops)))
		n, err := strconv.Atoi(raw)
		if err != nil || n < 1 || n > len(crops) {
			fmt.Printf("  Please enter a number between 1 and %d.\n", len(crops))
			continue
		}
		picked = crops[n-1]
		break
	}

	originalName := picked.Name
	fmt.Printf("\nEditing \"%s\" — press Enter to keep the current value.\n\n", originalName)

	// ── Crop name ───────────────────────────────────────────────────────

	nameInput := strings.TrimSpace(ask(fmt.Sprintf("Crop name [%s]: ", picked.Name)))
	if nameInput != "" {
		picked.Name = strings.ToLower(nameInput)
	}

	// ── Soak type ───────────────────────────────────────────────────────

	// Show the current soak setting and let the grower change it.
	currentSoak := "none"
	if picked.OvernightSoak {
		currentSoak = "overnight"
	} else if picked.SoakHours > 0 {
		currentSoak = fmt.Sprintf("same-day (%dh)", picked.SoakHours)
	}

	soakChoice := strings.ToLower(ask(fmt.Sprintf(
		"Soak type — (n)one, (s)ame-day hours, (o)vernight [%s]: ", currentSoak)))
	switch soakChoice {
	case "n":
		picked.OvernightSoak = false
		picked.SoakHours = 0
	case "s":
		picked.OvernightSoak = false
		picked.SoakHours = askInt(fmt.Sprintf("  Soak hours [%d]: ", picked.SoakHours), picked.SoakHours)
	case "o":
		picked.OvernightSoak = true
		picked.SoakHours = 12
	case "":
		// Keep current values
	}

	// ── Numeric parameters ──────────────────────────────────────────────

	picked.SeedGrams = askInt(fmt.Sprintf("Seed grams per tray [%d]: ", picked.SeedGrams), picked.SeedGrams)
	picked.DirtLitres = askFloat(fmt.Sprintf("Dirt litres per tray [%.1f]: ", picked.DirtLitres), picked.DirtLitres)
	picked.YieldGrams = askInt(fmt.Sprintf("Expected yield grams per tray [%d]: ", picked.YieldGrams), picked.YieldGrams)

	// ── Costing parameters (optional) ───────────────────────────────────

	fmt.Println()
	fmt.Println("Costing (press Enter to keep current values):")
	picked.SeedCost = askFloat(fmt.Sprintf("  Seed bag cost in $ [%.2f]: ", picked.SeedCost), picked.SeedCost)

	// Show seed bag weight in a friendly unit (kg if ≥1000 and even).
	currentWeightDisplay := fmt.Sprintf("%dg", picked.SeedPurchaseWeight)
	if picked.SeedPurchaseWeight >= 1000 && picked.SeedPurchaseWeight%1000 == 0 {
		currentWeightDisplay = fmt.Sprintf("%dkg", picked.SeedPurchaseWeight/1000)
	}
	picked.SeedPurchaseWeight = askInt(fmt.Sprintf("  Seed bag weight [%s]: ", currentWeightDisplay), picked.SeedPurchaseWeight)
	// Ask what unit — only if the grower typed a new value (not just Enter).
	// We detect this by checking if the value changed from before.
	unit := strings.ToLower(ask("  Weight unit — (g)rams or (k)g [g]: "))
	if unit == "k" || unit == "kg" {
		picked.SeedPurchaseWeight *= 1000
	}

	picked.UnitWeight = askInt(fmt.Sprintf("  Grams per sellable unit [%d]: ", picked.UnitWeight), picked.UnitWeight)
	picked.UnitSellPrice = askFloat(fmt.Sprintf("  Sell price per unit in $ [%.2f]: ", picked.UnitSellPrice), picked.UnitSellPrice)
	fmt.Println()

	// ── Total cycle days ────────────────────────────────────────────────

	newTotalDays := askInt(fmt.Sprintf("Total cycle days [%d]: ", picked.CycleDays), picked.CycleDays)
	if newTotalDays < 2 {
		fmt.Println("  A crop needs at least 2 days — keeping current value.")
		newTotalDays = picked.CycleDays
	}

	// ── Day-by-day tasks ────────────────────────────────────────────────
	//
	// If the total days changed, we rebuild the day list. If it stayed the
	// same, we walk through the existing days and let the grower tweak each.

	fmt.Println()
	fmt.Println("Now review each day's stage and tasks.")
	fmt.Println("Stages: (s)ow, (d)ark, (l)ight, (h)arvest")
	fmt.Println("Press Enter to keep the current value.")
	fmt.Println()

	startDay := 1
	if picked.OvernightSoak {
		startDay = 0
	}

	// Build a lookup of existing days by day number for quick access.
	existingByDay := make(map[int]crop.CropDay, len(picked.Days))
	for _, d := range picked.Days {
		existingByDay[d.Day] = d
	}

	var days []crop.CropDay
	darkDays := 0
	lightDays := 0

	for day := startDay; day <= newTotalDays; day++ {
		existing, hasExisting := existingByDay[day]

		// Determine the default stage: use existing if we have it,
		// otherwise generate a sensible one.
		defaultStage := "dark"
		defaultLetter := "d"
		if hasExisting {
			defaultStage = existing.Stage
			switch defaultStage {
			case "sow":
				defaultLetter = "s"
			case "dark":
				defaultLetter = "d"
			case "light":
				defaultLetter = "l"
			case "harvest":
				defaultLetter = "h"
			}
		} else if day == startDay || (picked.OvernightSoak && day <= 1) {
			defaultStage = "sow"
			defaultLetter = "s"
		} else if day == newTotalDays {
			defaultStage = "harvest"
			defaultLetter = "h"
		} else {
			sowDays := 1
			if picked.OvernightSoak {
				sowDays = 2
			}
			middleDays := newTotalDays - sowDays - 1
			halfPoint := sowDays + (middleDays+1)/2
			if day > halfPoint {
				defaultStage = "light"
				defaultLetter = "l"
			}
		}

		stageInput := strings.ToLower(ask(fmt.Sprintf("  Day %d — stage [%s]: ", day, defaultLetter)))
		stage := defaultStage
		switch stageInput {
		case "s", "sow":
			stage = "sow"
		case "d", "dark":
			stage = "dark"
		case "l", "light":
			stage = "light"
		case "h", "harvest":
			stage = "harvest"
		case "":
			// Keep the default
		default:
			fmt.Printf("    Unknown stage %q — using %q\n", stageInput, defaultStage)
		}

		// Show current tasks as the default, or empty for new days.
		currentTasks := ""
		if hasExisting {
			currentTasks = existing.Tasks
		}

		taskPrompt := fmt.Sprintf("  Day %d — tasks", day)
		if currentTasks != "" {
			taskPrompt += fmt.Sprintf(" [%s]", currentTasks)
		}
		taskPrompt += ": "

		tasksInput := ask(taskPrompt)
		tasks := currentTasks
		if tasksInput != "" {
			tasks = tasksInput
		}

		days = append(days, crop.CropDay{
			Day:   day,
			Stage: stage,
			Tasks: tasks,
		})

		switch stage {
		case "dark":
			darkDays++
		case "light":
			lightDays++
		}
	}

	// ── Build the updated Crop and save ─────────────────────────────────

	updated := crop.Crop{
		Name:               picked.Name,
		CycleDays:          days[len(days)-1].Day,
		OvernightSoak:      picked.OvernightSoak,
		SoakHours:          picked.SoakHours,
		SeedGrams:          picked.SeedGrams,
		DirtLitres:         picked.DirtLitres,
		DarkDays:           darkDays,
		LightDays:          lightDays,
		YieldGrams:         picked.YieldGrams,
		SeedCost:           picked.SeedCost,
		SeedPurchaseWeight: picked.SeedPurchaseWeight,
		UnitWeight:         picked.UnitWeight,
		UnitSellPrice:      picked.UnitSellPrice,
		Days:               days,
	}

	if err := crop.ReplaceCrop(originalName, updated); err != nil {
		fmt.Printf("\nError: %v\n", err)
		os.Exit(1)
	}

	fmt.Printf("\nSaved changes to \"%s\".\n", updated.Name)

	// Sync to Google Sheets if enabled.
	cfg, _ := config.Load()
	if cfg.SheetsEnabled && cfg.SheetID != "" {
		fmt.Println("Syncing to Google Sheets...")
		gcal.SyncLocalToSheet(context.Background())
	}
}
