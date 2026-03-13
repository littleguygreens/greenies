package main

import (
	"fmt"
	"os" // for os.Exit

	"github.com/littleguygreens/greenies/internal/farm"
	"github.com/littleguygreens/greenies/internal/store"
	"github.com/littleguygreens/greenies/internal/task"
)

// runClear deletes every task and cycle record after asking for confirmation.
// This resets both the calendar (tasks.json) and the snapshot (cycles.json)
// so the two data files stay in sync. The harvest log (harvests.json) is
// permanent history and is NOT cleared — use a text editor to edit that file
// directly if you ever need to remove a harvest record.
func runClear() {
	fmt.Print("This will delete ALL tasks and cycle records. Type \"yes\" to confirm: ")

	var response string
	fmt.Scanln(&response)

	if response != "yes" {
		fmt.Println("Cancelled — nothing was deleted.")
		return
	}

	// Clear the calendar tasks.
	if err := store.Save([]task.Task{}); err != nil {
		fmt.Printf("Error clearing tasks: %v\n", err)
		os.Exit(1)
	}

	// Also clear the cycle records so the snapshot doesn't show stale data.
	// Tasks and cycles are always created together (by "greenies plan"), so
	// wiping one without the other leaves the snapshot showing batches that
	// have no calendar tasks behind them.
	if err := farm.SaveCycles([]farm.Cycle{}); err != nil {
		fmt.Printf("Error clearing cycle records: %v\n", err)
		os.Exit(1)
	}

	fmt.Println("All tasks and cycle records deleted.")
	fmt.Println("(Harvest log preserved — run \"greenies harvestlog\" to see it.)")
}
