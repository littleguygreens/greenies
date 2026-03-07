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

// ConsoleExporter prints tasks to the terminal, one per line.
// It satisfies the Exporter interface because it has an Export method with
// the correct signature.
type ConsoleExporter struct{}

// Export prints each task in the list to the terminal in a simple format.
// This satisfies the Exporter interface and will be wired into the display
// path in Phase 5 when the Google Calendar exporter is added alongside it.
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
