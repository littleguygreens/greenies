// tentative.go — shared functions for managing tentative calendar tasks.
//
// When a grower starts a trial, the system places two "tentative" tasks on
// the calendar: one for the expected move-to-light date and one for the
// expected harvest date. These markers help the grower see upcoming trial
// milestones alongside their regular production schedule.
//
// The four functions in this file are used by both the CLI (cmd_trial.go)
// and the GUI (handlers_trial.go). They were originally copy-pasted in
// both places — now they live here once so any fix or change applies
// everywhere automatically.
package trial

import (
	"time"

	"github.com/littleguygreens/greenies/internal/store"
	"github.com/littleguygreens/greenies/internal/task"
)

// CreateTentativeTask creates a calendar task with an "(unconfirmed)" label
// for one tentative trial milestone (move-to-light or harvest).
//
// Parameters:
//   - displayName: the trial's human-readable name, e.g. "Mustard (seed lot xyz)"
//   - eventLabel:  what the milestone is, e.g. "move to light" or "harvest"
//   - dateStr:     the expected date in YYYY-MM-DD format
//
// Returns the new task's unique ID (stored in the TrialRecord so we can find
// the task later to update or delete it).
func CreateTentativeTask(displayName, eventLabel, dateStr string) (string, error) {
	title := displayName + " — " + eventLabel + "? (unconfirmed)"
	// task.New generates a unique ID and timestamps the task automatically.
	t, err := task.New(title, dateStr, "trial tentative marker")
	if err != nil {
		return "", err
	}
	existing, err := store.Load()
	if err != nil {
		return "", err
	}
	if err := store.Save(append(existing, t)); err != nil {
		return "", err
	}
	return t.ID, nil
}

// RefreshTentativeTasks inspects the current state of a trial and updates the
// titles of its tentative calendar tasks to reflect what has happened.
//
// There are three possible outcomes for each tentative task:
//
//   - The event was confirmed (a "light" or "harvest" stage day was logged,
//     or the harvest outcome was recorded): the title updates to a clean
//     confirmation, e.g. "Mustard — moved to light".
//
//   - The expected date passed without confirmation: the title changes from
//     "(unconfirmed)" to "(overdue)" so the grower sees the slip on their
//     calendar and knows to investigate.
//
//   - Neither condition applies: the task is left as-is.
//
// Only writes to disk when a title actually needs to change — safe to call
// on every manage session without unnecessary disk writes.
func RefreshTentativeTasks(tr *TrialRecord, today time.Time) error {
	if tr.TentativeMTLTaskID == "" && tr.TentativeHarvestTaskID == "" {
		// This trial has no tentative tasks — nothing to do.
		return nil
	}

	tasks, err := store.Load()
	if err != nil {
		return err
	}

	changed := false

	// ── Move-to-light task ────────────────────────────────────────────────────
	//
	// The move-to-light event is confirmed when the grower logs any day with
	// stage="light" in the manage flow — that is the moment trays physically
	// moved off the blackout shelf onto a lit rack.

	if tr.TentativeMTLTaskID != "" {
		mtlConfirmed := false
		for _, cd := range tr.ConfirmedDays {
			if cd.Stage == "light" {
				mtlConfirmed = true
				break
			}
		}

		// Decide what the title should say now.
		var newTitle string
		if mtlConfirmed {
			newTitle = tr.DisplayName() + " — moved to light"
		} else {
			// Not confirmed yet. Has the expected date already passed?
			mtlDateStr := tr.TentativeMoveToLightDate()
			if mtlDateStr != "" {
				mtlDate, parseErr := time.Parse(task.DateFormat, mtlDateStr)
				// today.After(mtlDate) means today is strictly past the expected day.
				if parseErr == nil && today.After(mtlDate) {
					newTitle = tr.DisplayName() + " — move to light? (overdue)"
				}
			}
		}

		// If the title needs updating, find the task by ID and change it.
		if newTitle != "" {
			for i, t := range tasks {
				if t.ID == tr.TentativeMTLTaskID {
					if tasks[i].Title != newTitle {
						tasks[i].Title = newTitle
						tasks[i].UpdatedAt = time.Now()
						changed = true
					}
					break
				}
			}
		}
	}

	// ── Harvest task ──────────────────────────────────────────────────────────
	//
	// The harvest event is confirmed when the trial status is set to harvested
	// or promoted (via the harvest outcome flow), OR when a day with
	// stage="harvest" is confirmed in the manage flow. Any of these means the
	// crop was actually cut.

	if tr.TentativeHarvestTaskID != "" {
		harvestConfirmed := tr.Status == StatusHarvested || tr.Status == StatusPromoted
		if !harvestConfirmed {
			for _, cd := range tr.ConfirmedDays {
				if cd.Stage == "harvest" {
					harvestConfirmed = true
					break
				}
			}
		}

		var newTitle string
		if harvestConfirmed {
			newTitle = tr.DisplayName() + " — harvested"
		} else {
			harvDateStr := tr.TentativeHarvestDate()
			if harvDateStr != "" {
				harvDate, parseErr := time.Parse(task.DateFormat, harvDateStr)
				if parseErr == nil && today.After(harvDate) {
					newTitle = tr.DisplayName() + " — harvest? (overdue)"
				}
			}
		}

		if newTitle != "" {
			for i, t := range tasks {
				if t.ID == tr.TentativeHarvestTaskID {
					if tasks[i].Title != newTitle {
						tasks[i].Title = newTitle
						tasks[i].UpdatedAt = time.Now()
						changed = true
					}
					break
				}
			}
		}
	}

	if changed {
		return store.Save(tasks)
	}
	return nil
}

// CancelTentativeTasks updates the tentative calendar tasks for a failed trial
// to show "(cancelled)" instead of "(unconfirmed)" or "(overdue)".
//
// The tasks are intentionally left on the calendar rather than deleted — they
// serve as a record that those milestones were planned but the trial did not
// reach them. The grower can delete them manually when they are ready.
func CancelTentativeTasks(tr *TrialRecord) error {
	if tr.TentativeMTLTaskID == "" && tr.TentativeHarvestTaskID == "" {
		return nil
	}

	tasks, err := store.Load()
	if err != nil {
		return err
	}

	changed := false
	for i, t := range tasks {
		switch t.ID {
		case tr.TentativeMTLTaskID:
			newTitle := tr.DisplayName() + " — move to light? (cancelled)"
			if tasks[i].Title != newTitle {
				tasks[i].Title = newTitle
				tasks[i].UpdatedAt = time.Now()
				changed = true
			}
		case tr.TentativeHarvestTaskID:
			newTitle := tr.DisplayName() + " — harvest? (cancelled)"
			if tasks[i].Title != newTitle {
				tasks[i].Title = newTitle
				tasks[i].UpdatedAt = time.Now()
				changed = true
			}
		}
	}

	if changed {
		return store.Save(tasks)
	}
	return nil
}

// RemoveTentativeTasks deletes the tentative calendar tasks for a discarded
// trial from tasks.json entirely. When a trial is fully discarded, all traces
// of it are erased — including any calendar markers placed at the start.
func RemoveTentativeTasks(tr *TrialRecord) error {
	if tr.TentativeMTLTaskID == "" && tr.TentativeHarvestTaskID == "" {
		return nil
	}

	existing, err := store.Load()
	if err != nil {
		return err
	}

	// Keep every task whose ID does not match either tentative marker.
	var remaining []task.Task
	for _, t := range existing {
		if t.ID == tr.TentativeMTLTaskID || t.ID == tr.TentativeHarvestTaskID {
			continue // this is a tentative task — skip it (i.e. delete it)
		}
		remaining = append(remaining, t)
	}
	return store.Save(remaining)
}
