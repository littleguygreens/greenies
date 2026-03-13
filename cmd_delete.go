package main

import (
	"fmt"
	"os"      // for os.Exit
	"strings" // for strings.ToLower

	"github.com/littleguygreens/greenies/internal/farm"
	"github.com/littleguygreens/greenies/internal/store"
	"github.com/littleguygreens/greenies/internal/task"
)

// runDelete handles the "greenies delete" command.
// It removes a task by ID. If the task belongs to a planned crop cycle
// (i.e. it has a CycleID), the user is offered the choice to delete just
// that one task or the entire cycle at once.
func runDelete() {
	fmt.Print("Task ID to delete: ")
	var id string
	fmt.Scanln(&id)
	if id == "" {
		fmt.Println("No ID entered — cancelled.")
		return
	}

	tasks, err := store.Load()
	if err != nil {
		fmt.Printf("Error loading tasks: %v\n", err)
		os.Exit(1)
	}

	// Find the target task so we can check whether it has a CycleID.
	var target *task.Task
	for i := range tasks {
		if tasks[i].ID == id {
			target = &tasks[i]
			break
		}
	}
	if target == nil {
		fmt.Printf("No task found with ID %q. Use \"greenies list\" to see task IDs.\n", id)
		os.Exit(1)
	}

	// Decide whether to delete just this one task or the whole cycle.
	// deleteByID is the set of task IDs we will actually remove.
	deleteByID := map[string]bool{id: true}

	if target.CycleID != "" {
		// Count how many tasks share this cycle so we can tell the user.
		cycleCount := 0
		for _, t := range tasks {
			if t.CycleID == target.CycleID {
				cycleCount++
			}
		}

		fmt.Printf("Task: %q (%s)\n", target.Title, target.Date)
		fmt.Printf("This task belongs to a planned cycle (%d tasks total).\n", cycleCount)
		fmt.Print("Delete just this task, or the whole cycle? [t/c]: ")

		var choice string
		fmt.Scanln(&choice)

		if strings.ToLower(choice) == "c" {
			// Mark every task in this cycle for deletion.
			for _, t := range tasks {
				if t.CycleID == target.CycleID {
					deleteByID[t.ID] = true
				}
			}
		}
	}

	// Build the kept list by skipping anything in the delete set.
	var updated []task.Task
	for _, t := range tasks {
		if !deleteByID[t.ID] {
			updated = append(updated, t)
		}
	}

	if err := store.Save(updated); err != nil {
		fmt.Printf("Error saving tasks: %v\n", err)
		os.Exit(1)
	}

	// If a whole cycle was deleted, also remove its record from cycles.json
	// so that "greenies snapshot" no longer shows the deleted batch.
	// A task-only deletion (not a full cycle) leaves cycles.json untouched
	// because the rest of the cycle is still active.
	if target.CycleID != "" && len(deleteByID) > 1 {
		// len(deleteByID) > 1 means we deleted more than one task, which
		// only happens when the user chose to delete the whole cycle ("c").
		cycles, cycleErr := farm.LoadCycles()
		if cycleErr == nil {
			var keptCycles []farm.Cycle
			for _, c := range cycles {
				if c.CycleID != target.CycleID {
					keptCycles = append(keptCycles, c)
				}
			}
			if err := farm.SaveCycles(keptCycles); err != nil {
				fmt.Printf("Warning: tasks deleted but could not update cycle records: %v\n", err)
			}
		}
	}

	removed := len(tasks) - len(updated)
	if removed == 1 {
		fmt.Printf("1 task deleted.\n")
	} else {
		fmt.Printf("%d tasks deleted.\n", removed)
	}
}
