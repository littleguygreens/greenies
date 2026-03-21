// handlers_swimlane.go — Swim-lane calendar helper and data types.
//
// buildSnapshotWeek builds one week of swim-lane data for the dashboard
// and snapshot pages. The month* types hold the structured data that the
// swim-lane templates render as coloured bars.
package gui

import (
	"fmt"
	"sort"
	"time"

	"github.com/littleguygreens/greenies/internal/crop"
	"github.com/littleguygreens/greenies/internal/farm"
	"github.com/littleguygreens/greenies/internal/task"
)

// ─────────────────────────────────────────────────────────────────────────────
// Month calendar with swim lanes
// ─────────────────────────────────────────────────────────────────────────────

// ─────────────────────────────────────────────────────────────────────────────
// Snapshot swim-lane helper
// ─────────────────────────────────────────────────────────────────────────────

// buildSnapshotWeek builds one week of swim-lane calendar data containing the
// given date. The highlighted date's column gets IsHighlighted = true so the
// template can visually mark it.
//
// This is used by the snapshot and dashboard pages to show a mini calendar
// view at the top — the same swim-lane style as the full month calendar,
// but just one week.
//
// The weekStartPref parameter controls which day the week starts on:
// "mon" for Monday–Sunday, anything else (including "") for Sunday–Saturday.
// This matches the setting from the Settings page.
func buildSnapshotWeek(focusDate time.Time, cycles []farm.Cycle, weekStartPref string) (monthWeek, [7]string) {
	// Pick the day labels and first-weekday based on the preference.
	var dayLabels [7]string
	var firstWeekday time.Weekday
	if weekStartPref == "mon" {
		dayLabels = [7]string{"Mon", "Tue", "Wed", "Thu", "Fri", "Sat", "Sun"}
		firstWeekday = time.Monday
	} else {
		dayLabels = [7]string{"Sun", "Mon", "Tue", "Wed", "Thu", "Fri", "Sat"}
		firstWeekday = time.Sunday
	}

	// Find the start of the calendar week containing the focus date.
	weekStart := focusDate
	for weekStart.Weekday() != firstWeekday {
		weekStart = weekStart.AddDate(0, 0, -1)
	}

	// Load the crop library so we can check soak settings.
	cropMap := map[string]crop.Crop{}
	if cropsSource, err := crop.GetSource(); err == nil {
		if allCrops, err := cropsSource.LoadCrops(); err == nil {
			for _, cr := range allCrops {
				cropMap[cr.Name] = cr
			}
		}
	}

	// Sort cycles oldest-first (same order as the full calendar).
	sort.Slice(cycles, func(i, j int) bool {
		if cycles[i].SowDate != cycles[j].SowDate {
			return cycles[i].SowDate < cycles[j].SowDate
		}
		return cycles[i].CropName < cycles[j].CropName
	})

	// Pre-compute each cycle's daily stage map — same logic as handleCalendar.
	type cycleInfo struct {
		CropName string
		Trays    int
		DayStage map[string]string
	}

	var allCycleInfo []cycleInfo
	for _, c := range cycles {
		sowDate, err1 := time.Parse(task.DateFormat, c.SowDate)
		harvestDate, err2 := time.Parse(task.DateFormat, c.HarvestDate)
		mtlDate, err3 := time.Parse(task.DateFormat, c.MoveToLightDate)
		if err1 != nil || err2 != nil || err3 != nil {
			continue
		}

		stages := map[string]string{}
		cr, hasCrop := cropMap[c.CropName]

		// Soak day(s).
		if hasCrop && cr.OvernightSoak {
			soakDay := sowDate.AddDate(0, 0, -1)
			stages[soakDay.Format(task.DateFormat)] = "soak"
		} else if hasCrop && cr.SoakHours > 0 {
			stages[sowDate.Format(task.DateFormat)] = "soak"
		}

		// Walk sow → harvest assigning stages.
		for d := sowDate; !d.After(harvestDate); d = d.AddDate(0, 0, 1) {
			ds := d.Format(task.DateFormat)
			if _, already := stages[ds]; already {
				continue
			}
			if d.Equal(harvestDate) {
				stages[ds] = "harvest"
			} else if d.Before(mtlDate) {
				stages[ds] = "dark"
			} else {
				stages[ds] = "light"
			}
		}

		allCycleInfo = append(allCycleInfo, cycleInfo{
			CropName: c.CropName,
			Trays:    c.Trays,
			DayStage: stages,
		})
	}

	// Build the single week.
	week := monthWeek{}

	// Fill in the 7 column headers.
	for i := 0; i < 7; i++ {
		d := weekStart.AddDate(0, 0, i)
		week.Headers[i] = monthDayHeader{
			DayNum:        d.Day(),
			InMonth:       true, // always "in month" for snapshot view
			IsHighlighted: d.Equal(focusDate),
			DateStr:       d.Format(task.DateFormat),
		}
	}

	// Build a swim-lane row for each cycle that has activity this week.
	for _, ci := range allCycleInfo {
		row := monthCycleRow{
			CropName: ci.CropName,
			Trays:    ci.Trays,
		}
		hasActivity := false
		prevStage := ""

		for i := 0; i < 7; i++ {
			d := weekStart.AddDate(0, 0, i)
			ds := d.Format(task.DateFormat)
			stage := ci.DayStage[ds]

			label := ""
			if stage != "" && stage != prevStage {
				label = fmt.Sprintf("%s %dx", ci.CropName, ci.Trays)
			}

			row.Cells[i] = monthCell{
				DayNum:        d.Day(),
				Stage:         stage,
				Label:         label,
				Trays:         ci.Trays,
				InMonth:       true,
				IsHighlighted: d.Equal(focusDate),
				DateStr:       ds,
			}

			prevStage = stage
			if stage != "" {
				hasActivity = true
			}
		}

		if hasActivity {
			week.Rows = append(week.Rows, row)
		}
	}

	return week, dayLabels
}

// monthCell holds the display info for one day-cell in the month calendar grid.
// Each cell belongs to a specific cycle on a specific day. The template uses
// the Stage field to pick the background colour.
type monthCell struct {
	// DayNum is the calendar day number (1–31). Zero means this cell is
	// outside the current month (padding at the start or end of the grid).
	DayNum int

	// Stage is which phase of the crop cycle falls on this day:
	// "soak", "dark", "light", "harvest", or "" (no activity).
	Stage string

	// Label is the text shown on the first cell of each stage run — e.g.
	// "sunnies 16x — dark". Empty on continuation cells (2nd, 3rd day
	// of the same stage) so the text only appears once at the start.
	Label string

	// Trays is the batch size — shown as a tooltip or label so the grower
	// can tell overlapping batches apart.
	Trays int

	// InMonth is true if this cell falls within the displayed month.
	// False for padding cells at the start/end of the grid. The template
	// dims or hides these cells so they don't distract.
	InMonth bool

	// IsHighlighted is true if this cell is the "focus" day — used on the
	// snapshot page to highlight which day the grower is looking at.
	IsHighlighted bool

	// DateStr is the full date in YYYY-MM-DD format (e.g. "2026-03-19").
	// Used to build a clickable link so tapping a cell takes the grower
	// to the snapshot for that day.
	DateStr string
}

// monthCycleRow is one swim-lane row in a week section. It represents one
// crop cycle's activity across 7 days (Monday to Sunday).
type monthCycleRow struct {
	// CropName is the variety label shown in the left column.
	CropName string

	// Trays is the batch size — appended to the label so the grower can
	// distinguish "sunnies 16x" from "sunnies 12x" in the same week.
	Trays int

	// Cells holds exactly 7 entries, one per day (Monday index 0 through
	// Sunday index 6). Each cell is either coloured by stage or empty.
	Cells [7]monthCell
}

// monthDayHeader holds the info for one column header in a week row.
type monthDayHeader struct {
	// DayNum is the calendar day number (1–31).
	DayNum int

	// InMonth is true if this day falls within the displayed month.
	InMonth bool

	// IsHighlighted is true if this column is the "focus" day — used
	// on the snapshot page to highlight the snapshot date's column.
	IsHighlighted bool

	// DateStr is the full date in YYYY-MM-DD format (e.g. "2026-03-19").
	// Used to build a clickable link from the header to the snapshot page.
	DateStr string
}

// monthWeek groups all the cycle rows that have activity in one calendar week.
type monthWeek struct {
	// Headers holds 7 entries (Mon–Sun) with the date number and
	// whether that day is inside the displayed month.
	Headers [7]monthDayHeader

	// Rows holds one swim-lane row per cycle that has activity this week.
	// Sorted oldest cycle first (by sow date).
	Rows []monthCycleRow
}
