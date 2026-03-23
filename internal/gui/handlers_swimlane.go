// handlers_swimlane.go — Swim-lane calendar helper and data types.
//
// buildSnapshotWeek builds one week of swim-lane data for the dashboard
// and snapshot pages. The month* types hold the structured data that the
// swim-lane templates render as coloured bars.
package gui

import (
	"fmt"
	"sort"
	"strings"
	"time"

	"github.com/littleguygreens/greenies/internal/crop"
	"github.com/littleguygreens/greenies/internal/farm"
	"github.com/littleguygreens/greenies/internal/task"
)

// ─────────────────────────────────────────────────────────────────────────────
// Shared cycle-stage computation
// ─────────────────────────────────────────────────────────────────────────────

// swimCycleInfo holds one cycle's daily stage map — a lookup from calendar
// date to stage name ("soak", "dark", "light", "harvest", or ""). This is
// pre-computed once and then used by both the full-month calendar and the
// snapshot-week mini calendar so the stage-assignment logic lives in one place.
type swimCycleInfo struct {
	CropName    string
	Trays       int
	SowDate     string            // YYYY-MM-DD — used by label builder for date display
	HarvestDate string            // YYYY-MM-DD — used by label builder for date display
	DayStage    map[string]string // date (YYYY-MM-DD) → stage name
}

// buildCycleStages takes a list of farm cycles and a crop lookup map, sorts
// the cycles oldest-first, and returns a pre-computed stage map for each one.
//
// Both the full-month calendar (handleCalendar) and the snapshot-week helper
// (buildSnapshotWeek) call this so the date-walking and stage-assignment
// logic is never duplicated.
func buildCycleStages(cycles []farm.Cycle, cropMap map[string]crop.Crop) []swimCycleInfo {
	// Sort cycles oldest-first so swim-lane rows appear in a stable,
	// predictable order (earliest sow date at the top).
	sort.Slice(cycles, func(i, j int) bool {
		if cycles[i].SowDate != cycles[j].SowDate {
			return cycles[i].SowDate < cycles[j].SowDate
		}
		return cycles[i].CropName < cycles[j].CropName
	})

	var result []swimCycleInfo

	for _, c := range cycles {
		sowDate, err1 := time.Parse(task.DateFormat, c.SowDate)
		harvestDate, err2 := time.Parse(task.DateFormat, c.HarvestDate)
		mtlDate, err3 := time.Parse(task.DateFormat, c.MoveToLightDate)
		if err1 != nil || err2 != nil || err3 != nil {
			continue
		}

		stages := map[string]string{}
		cr, hasCrop := cropMap[c.CropName]

		// Soak day(s) — before the blackout bar starts.
		if hasCrop && cr.OvernightSoak {
			soakDay := sowDate.AddDate(0, 0, -1)
			stages[soakDay.Format(task.DateFormat)] = "soak"
		} else if hasCrop && cr.SoakHours > 0 {
			stages[sowDate.Format(task.DateFormat)] = "soak"
		}

		// Walk from sow date to harvest date, assigning stages.
		for d := sowDate; !d.After(harvestDate); d = d.AddDate(0, 0, 1) {
			ds := d.Format(task.DateFormat)
			if _, already := stages[ds]; already {
				continue // soak day already set
			}
			if d.Equal(harvestDate) {
				stages[ds] = "harvest"
			} else if d.Before(mtlDate) {
				stages[ds] = "dark"
			} else {
				stages[ds] = "light"
			}
		}

		result = append(result, swimCycleInfo{
			CropName:    c.CropName,
			Trays:       c.Trays,
			SowDate:     c.SowDate,
			HarvestDate: c.HarvestDate,
			DayStage:    stages,
		})
	}

	return result
}

// ─────────────────────────────────────────────────────────────────────────────
// Responsive swim-lane labels
// ─────────────────────────────────────────────────────────────────────────────

// swimLabel picks the right label text for a crop row based on how many
// cells the crop occupies this week (the span). Wider spans get more
// detail; a single cell gets just the crop name. The label is placed
// once per row and centered across all active cells.
//
// Three tiers:
//   span >= 3 : "sunnies 12x (mar 20 - mar 28)"
//   span == 2 : "sunnies 12x"
//   span == 1 : "sunnies"
func swimLabel(cropName string, trays int, sowDateStr, harvestDateStr string, span int) string {
	if span == 1 {
		return cropName
	}
	base := fmt.Sprintf("%s %dx", cropName, trays)
	if span <= 2 {
		return base
	}

	// Parse the sow and harvest dates so we can show short date labels.
	sowDate, err1 := time.Parse(task.DateFormat, sowDateStr)
	harvestDate, err2 := time.Parse(task.DateFormat, harvestDateStr)
	if err1 != nil || err2 != nil {
		return base // fall back if dates don't parse
	}

	// Short month+day format like "mar 20".
	sowShort := strings.ToLower(sowDate.Format("Jan 2"))
	harvestShort := strings.ToLower(harvestDate.Format("Jan 2"))

	return fmt.Sprintf("%s (%s - %s)", base, sowShort, harvestShort)
}

// placeRowLabel finds the first and last active cells in a 7-cell row,
// calculates the total span, picks the right label, and writes it onto
// the first active cell. This gives each swim-lane row exactly one
// centered label instead of one per stage transition.
func placeRowLabel(cells *[7]monthCell, cropName string, trays int, sowDate, harvestDate string) {
	// Find first and last active cell indices.
	first := -1
	last := -1
	for i := 0; i < 7; i++ {
		if cells[i].Stage != "" {
			if first == -1 {
				first = i
			}
			last = i
		}
	}
	if first < 0 {
		return // no activity this week
	}

	span := last - first + 1
	cells[first].Label = swimLabel(cropName, trays, sowDate, harvestDate, span)
	cells[first].Span = span
}

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

	// Load the crop library so we can check soak settings, then build the
	// pre-computed stage map for each cycle using the shared helper.
	cropMap := map[string]crop.Crop{}
	if cropsSource, err := crop.GetSource(); err == nil {
		if allCrops, err := cropsSource.LoadCrops(); err == nil {
			for _, cr := range allCrops {
				cropMap[cr.Name] = cr
			}
		}
	}

	allCycleInfo := buildCycleStages(cycles, cropMap)

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

		for i := 0; i < 7; i++ {
			d := weekStart.AddDate(0, 0, i)
			ds := d.Format(task.DateFormat)
			stage := ci.DayStage[ds]

			row.Cells[i] = monthCell{
				DayNum:        d.Day(),
				Stage:         stage,
				Trays:         ci.Trays,
				InMonth:       true,
				IsHighlighted: d.Equal(focusDate),
				DateStr:       ds,
			}

			if stage != "" {
				hasActivity = true
			}
		}

		// Place one centered label per row across all active cells.
		placeRowLabel(&row.Cells, ci.CropName, ci.Trays, ci.SowDate, ci.HarvestDate)

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

	// Span is how many consecutive cells of the same stage follow this one
	// (including this cell). For example, if a crop is in "light" for days
	// 3–5 of the week, the first cell gets Span=3, the others get Span=0.
	// Used by CSS to size the label so it doesn't overflow into other rows.
	Span int
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
