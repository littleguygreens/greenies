package main

import (
	"fmt"
	"os"      // for os.Exit
	"strings" // for strings.Repeat

	"github.com/littleguygreens/greenies/internal/crop"
)

// runCrops handles the "greenies crops" command.
// It reads the crop library and prints a summary of every available variety.
func runCrops() {
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

	for _, c := range crops {
		// Show the key numbers at a glance: cycle length, seed and yield weights.
		fmt.Printf("  %-12s  %d days   seed: %dg/tray   yield: %dg/tray\n",
			c.Name, c.CycleDays, c.SeedGrams, c.YieldGrams)
	}

	fmt.Println()
	fmt.Println("Run \"greenies plan\" to plan a crop cycle.")
}
