// This file contains the ConsoleExporter — the Phase 1 implementation of the
// Exporter interface. It simply prints a list of tasks to the terminal.
//
// In later phases this will be joined by other exporters (GoogleCalendar, CSV)
// but this one will always remain useful for quick debugging and for users who
// prefer the terminal over external integrations.
package export

import (
	"fmt"

	"github.com/littleguygreens/greenies/internal/task"
)

// ConsoleExporter is kept as a working example of how to add a new output
// destination to the Exporter interface. To add a new exporter (e.g. CSV,
// email), copy this file, change the Export method, and append your new
// exporter to the list before calling RunAll.
type ConsoleExporter struct{}

// Export prints each task in the list to the terminal in a simple format.
func (c ConsoleExporter) Export(tasks []task.Task) error {
	for _, t := range tasks {
		// Print a compact single-line summary of the task.
		// The format is: [id] YYYY-MM-DD  Title  (Notes)
		// Notes are only shown if the task has them.
		if t.Notes != "" {
			fmt.Printf("  [%s]  %s  %s  (%s)\n", t.ID, t.Date, t.Title, t.Notes)
		} else {
			fmt.Printf("  [%s]  %s  %s\n", t.ID, t.Date, t.Title)
		}
	}
	return nil
}
