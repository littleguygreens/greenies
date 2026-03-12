// Package export defines the standard shape that any output destination must
// follow, and provides the Phase 1 implementation that prints to the terminal.
//
// Why does this exist?
// Right now we only need to display tasks in the terminal. But in future phases
// we will want to send tasks to Google Calendar, export them as a CSV file, or
// push them to other services. Rather than rewriting the core scheduling logic
// for each output, we define one standard "plug shape" here. Any new output
// just needs to fit that shape — the rest of the program never changes.
//
// This pattern is called "programming to an interface" and is one of the most
// useful tools in Go for keeping a codebase flexible and manageable.
package export

import (
	"fmt"

	"github.com/littleguygreens/greenies/internal/task"
)

// Exporter is the standard plug shape that every output destination must match.
//
// To add a new output in a future phase (e.g. Google Calendar, CSV), create a
// new type in this package and give it an Export method with this exact
// signature. That is all that is required — no changes anywhere else.
//
// Export receives a list of tasks and is responsible for doing something useful
// with them — printing them, sending them to an API, writing them to a file, etc.
// It returns an error if something goes wrong, or nil if everything succeeded.
type Exporter interface {
	Export(tasks []task.Task) error
}

// RunAll calls Export on every exporter in the list, passing the same tasks
// to each one. This is the central "plug-in point" — adding a new output
// destination (e.g. Google Calendar) is as simple as appending it to the
// list before calling RunAll.
//
// If one exporter fails (e.g. Google Calendar is offline), a warning is
// printed but the remaining exporters still run. A network problem with one
// output should never prevent the others from working.
func RunAll(exporters []Exporter, tasks []task.Task) {
	for _, e := range exporters {
		if err := e.Export(tasks); err != nil {
			fmt.Printf("Warning: an exporter failed: %v\n", err)
		}
	}
}
