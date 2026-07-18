// Package trial manages experimental crop runs — the trialling system.
//
// Think of this package as a lab notebook for the farm. When you want to try
// a new crop variety, or test whether changing the seed density or soak time
// makes a difference, you start a trial. The trial lives separately from your
// confirmed crop library (crops.csv) so your production schedule is never
// disturbed by experiments.
//
// A trial has a simple lifecycle:
//
//	active    → you started a trial and it is currently growing
//	harvested → the crop reached harvest and you recorded the yield
//	promoted  → you were happy with the results; parameters copied to crops.csv
//	failed    → the crop failed mid-cycle (mould, bad germination, etc.)
//	discarded → you deleted all data for this trial run
//
// Two data files are maintained:
//
//	~/.greenies/trials.json  — all trial records including observations and
//	                           confirmed day parameters (internal storage)
//	~/.greenies/trials.csv   — the confirmed parameters in human-readable format,
//	                           regenerated automatically every time trials.json is
//	                           saved. Open in Google Sheets to review progress.
package trial

import (
	"encoding/csv"
	"encoding/json"
	"fmt"
	"math"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"github.com/littleguygreens/greenies/internal/crop"
	"github.com/littleguygreens/greenies/internal/task"
)

// ─────────────────────────────────────────────────────────────────────────────
// Status constants
// ─────────────────────────────────────────────────────────────────────────────

// The five states a trial can move through during its life.
// StatusDiscarded is never actually stored — when a trial is discarded, its
// record is simply removed from the list entirely.
const (
	StatusActive    = "active"    // trial is currently growing
	StatusHarvested = "harvested" // crop was cut and yield was recorded
	StatusPromoted  = "promoted"  // parameters were copied to crops.csv
	StatusFailed    = "failed"    // crop was lost (mould, bad germination, etc.)
	StatusDiscarded = "discarded" // all data deleted — this value is never saved to disk
)

// ─────────────────────────────────────────────────────────────────────────────
// Data types
// ─────────────────────────────────────────────────────────────────────────────

// TrialObservation holds the grower's free-text notes for one specific day
// of a trial. These are entered through the "manage" flow — either on the day
// itself, or caught up later by stepping through missed days in sequence.
//
// Over the course of a trial, observations build up a day-by-day story that
// the grower can review before starting a new trial of the same crop.
type TrialObservation struct {
	// Day is the cycle day number (Day 1 = sow date, Day 2 = the next day, etc.)
	Day int

	// Date is the actual calendar date for this observation. Format: YYYY-MM-DD.
	Date string

	// Notes is the grower's free-text entry for the day.
	// Examples: "looking leggy — moved grow light closer"
	//           "strong germination, almost all seed sprouted"
	//           "no tasks today, automated watering handling everything"
	Notes string
}

// TrialDayParams holds the confirmed growing instructions for one day of a
// trial — what stage the crop was at and what the grower actually did.
//
// These are collected day by day through the manage flow when the grower
// confirms "yes, here is what I did on Day 4". They get written to trials.csv
// and, if the trial is promoted, become the official rows in crops.csv.
type TrialDayParams struct {
	// Day is the cycle day number this row covers.
	Day int

	// Stage is the growing stage for this day.
	// One of: "sow", "dark", "light", "harvest".
	Stage string

	// Tasks is a comma-separated list of what the grower did.
	// Example: "bottom water, rotate stack"
	// Leave empty for do-nothing days (automated watering on lit racks, etc.)
	Tasks string
}

// TrialRecord holds everything we know about one trial run — from the moment
// it was created through to its final outcome.
//
// One TrialRecord represents one attempt to grow a particular crop variety
// under a particular set of conditions. Multiple records for the same crop
// name are treated as related runs: when you start a new mustard trial, the
// program shows you notes from any previous mustard trials so you can learn
// from past results.
type TrialRecord struct {
	// ID is a unique identifier, generated at creation time.
	// Same format as CycleID in the main scheduler — a short random hex string.
	ID string

	// CropName is the variety being trialled. Lower-case, e.g. "mustard".
	// Trials with the same CropName are treated as related experiments.
	CropName string

	// TrialVariable describes what is specifically being tested in this run.
	// Empty string means no particular variable — just a general first trial
	// of a new crop. When set, it appears in the display name in parentheses.
	// Example: CropName="sunnies", TrialVariable="seed lot xyz"
	//          → display name "Sunnies (seed lot xyz)"
	TrialVariable string

	// SowDate is Day 1 of the trial — the date the seed was sown.
	// For overnight-soak crops (where Day 0 is the soak day), SowDate is
	// the day after the soak, when trays are actually prepared and filled.
	// Format: YYYY-MM-DD.
	SowDate string

	// Trays is how many grow trays this trial occupies.
	Trays int

	// Status tracks where this trial is in its lifecycle.
	// One of the Status* constants above.
	Status string

	// LastManaged is the date of the most recent manage session.
	// The next manage session catches up from the day after this date.
	// Initialised to the day before SowDate when the trial is created, so
	// the very first manage session always starts at Day 1 (the sow date).
	// Format: YYYY-MM-DD.
	LastManaged string

	// ── Upfront parameters ────────────────────────────────────────────────────
	//
	// These are filled in when the trial is created, if the grower already
	// knows them. Zero / false = unknown at trial start.

	// OvernightSoak is true for crops that need an overnight seed soak on
	// the day before sowing (Day 0). Peas and daikon are examples.
	OvernightSoak bool

	// SoakHours is the duration of a same-day soak, in hours.
	// Zero means either no soak or an overnight soak — use OvernightSoak
	// to tell these two cases apart.
	SoakHours float64

	// SeedGrams is the seed weight per tray in grams.
	// Zero means the grower didn't record this at trial start.
	SeedGrams float64

	// MediumLitres is the volume of grow medium per tray in litres.
	// Zero means unknown; the default when entered is 1.
	MediumLitres float64

	// MoveToLightDay is the expected cycle day when trays move from blackout
	// to a lit rack. Zero = unknown.
	MoveToLightDay int

	// HarvestDay is the expected cycle day for harvest. Zero = unknown.
	HarvestDay int

	// ── Outcome fields ────────────────────────────────────────────────────────
	//
	// Set when the trial ends.

	// ActualYieldGrams is the total harvest weight in grams, entered when
	// the grower logs a harvest. Only meaningful for harvested/promoted trials.
	ActualYieldGrams int

	// FailureNote is the grower's explanation of what went wrong.
	// Only set for failed trials.
	FailureNote string

	// ── Per-day data ──────────────────────────────────────────────────────────

	// Observations is the free-text daily log, one entry per day managed.
	// Never edited after being written — each day's note is permanent.
	Observations []TrialObservation

	// ConfirmedDays is the list of days for which the grower has recorded
	// official parameters (stage + tasks). These rows end up in trials.csv
	// and, on promotion, get appended to crops.csv.
	ConfirmedDays []TrialDayParams

	// ── Tentative calendar task IDs ───────────────────────────────────────────
	//
	// When a trial is created with a known move-to-light day or harvest day,
	// the program immediately places a "(unconfirmed)" marker on the calendar
	// (e.g. "Mustard — move to light? (unconfirmed)"). These fields store the
	// IDs of those tasks so the manage flow can find and update them later —
	// changing the title to "(overdue)" if the day passes, or removing the
	// qualifier when the event is confirmed.
	//
	// Empty string means no tentative task was created for that milestone.

	// TentativeMTLTaskID is the ID of the move-to-light "(unconfirmed)" task.
	TentativeMTLTaskID string

	// TentativeHarvestTaskID is the ID of the harvest "(unconfirmed)" task.
	TentativeHarvestTaskID string
}

// ─────────────────────────────────────────────────────────────────────────────
// TrialRecord helper methods
// ─────────────────────────────────────────────────────────────────────────────

// DisplayName returns the human-readable name for this trial.
// If a TrialVariable is set, it appears in parentheses after the crop name.
//
// Examples:
//
//	CropName="mustard",  TrialVariable=""            → "Mustard"
//	CropName="sunnies",  TrialVariable="seed lot xyz" → "Sunnies (seed lot xyz)"
func (tr TrialRecord) DisplayName() string {
	name := task.Capitalize(tr.CropName)
	if tr.TrialVariable != "" {
		return name + " (" + tr.TrialVariable + ")"
	}
	return name
}

// DayNumber returns what day of the cycle the given date falls on,
// counting from Day 1 = SowDate. Returns 0 if the date is before the sow date.
//
// The time-of-day is stripped from the input so that a "today at 3pm" input
// compares correctly against the midnight-UTC dates stored in SowDate.
func (tr TrialRecord) DayNumber(today time.Time) int {
	sow, err := time.Parse(task.DateFormat, tr.SowDate)
	if err != nil {
		return 0
	}
	// Normalise today to midnight UTC so the subtraction is always a whole
	// number of days — no partial-day rounding surprises.
	t := time.Date(today.Year(), today.Month(), today.Day(), 0, 0, 0, 0, time.UTC)
	days := int(t.Sub(sow).Hours()/24) + 1
	if days < 1 {
		return 0
	}
	return days
}

// TentativeHarvestDate returns the estimated harvest date as a YYYY-MM-DD
// string, computed as SowDate + (HarvestDay - 1) days.
// Returns "" if HarvestDay is zero (unknown).
func (tr TrialRecord) TentativeHarvestDate() string {
	if tr.HarvestDay == 0 {
		return ""
	}
	sow, err := time.Parse(task.DateFormat, tr.SowDate)
	if err != nil {
		return ""
	}
	return sow.AddDate(0, 0, tr.HarvestDay-1).Format(task.DateFormat)
}

// TentativeMoveToLightDate returns the estimated move-to-light date as a
// YYYY-MM-DD string, computed as SowDate + (MoveToLightDay - 1) days.
// Returns "" if MoveToLightDay is zero (unknown).
func (tr TrialRecord) TentativeMoveToLightDate() string {
	if tr.MoveToLightDay == 0 {
		return ""
	}
	sow, err := time.Parse(task.DateFormat, tr.SowDate)
	if err != nil {
		return ""
	}
	return sow.AddDate(0, 0, tr.MoveToLightDay-1).Format(task.DateFormat)
}

// IsActive returns true if this trial is currently growing (status = "active").
// Convenience helper so callers don't have to compare status strings directly.
func (tr TrialRecord) IsActive() bool {
	return tr.Status == StatusActive
}

// ─────────────────────────────────────────────────────────────────────────────
// File paths
// ─────────────────────────────────────────────────────────────────────────────

// dataDir returns the path to the shared greenies data folder.
// On Linux this is typically /home/<username>/.greenies.
func dataDir() (string, error) {
	home, err := os.UserHomeDir()
	if err != nil {
		return "", fmt.Errorf("could not find home directory: %w", err)
	}
	return filepath.Join(home, ".greenies"), nil
}

// TrialsJSONPath returns the full path to the trial metadata JSON file.
// Example: /home/farm/.greenies/trials.json
func TrialsJSONPath() (string, error) {
	dir, err := dataDir()
	if err != nil {
		return "", err
	}
	return filepath.Join(dir, "trials.json"), nil
}

// TrialsCSVPath returns the full path to the trial parameters CSV file.
// Example: /home/farm/.greenies/trials.csv
func TrialsCSVPath() (string, error) {
	dir, err := dataDir()
	if err != nil {
		return "", err
	}
	return filepath.Join(dir, "trials.csv"), nil
}

// ─────────────────────────────────────────────────────────────────────────────
// Load and save
// ─────────────────────────────────────────────────────────────────────────────

// LoadTrials reads all trial records from ~/.greenies/trials.json.
// Returns an empty list if the file doesn't exist yet (e.g. on first run,
// or before any trials have been started) — same friendly behaviour as
// farm.LoadCycles() for new installs.
func LoadTrials() ([]TrialRecord, error) {
	path, err := TrialsJSONPath()
	if err != nil {
		return nil, err
	}

	data, err := os.ReadFile(path)
	if err != nil {
		if os.IsNotExist(err) {
			return []TrialRecord{}, nil
		}
		return nil, fmt.Errorf("could not read trials file: %w", err)
	}

	var records []TrialRecord
	if err := json.Unmarshal(data, &records); err != nil {
		return nil, fmt.Errorf("trials file appears to be corrupted: %w", err)
	}

	return records, nil
}

// SaveTrials writes all trial records to trials.json and regenerates
// trials.csv from the confirmed parameters embedded in those records.
//
// Both files are always rewritten in full — simple and safe for the number
// of trials a single farm will ever run.
func SaveTrials(records []TrialRecord) error {
	dir, err := dataDir()
	if err != nil {
		return err
	}
	if err := os.MkdirAll(dir, 0755); err != nil {
		return fmt.Errorf("could not create data directory: %w", err)
	}

	// ── Write trials.json ──────────────────────────────────────────────────────
	jsonPath, err := TrialsJSONPath()
	if err != nil {
		return err
	}
	data, err := json.MarshalIndent(records, "", "  ")
	if err != nil {
		return fmt.Errorf("could not encode trial records: %w", err)
	}
	if err := os.WriteFile(jsonPath, data, 0644); err != nil {
		return fmt.Errorf("could not write trials file: %w", err)
	}

	// ── Regenerate trials.csv ──────────────────────────────────────────────────
	return writeTrialsCSV(records)
}

// ─────────────────────────────────────────────────────────────────────────────
// CSV generation
// ─────────────────────────────────────────────────────────────────────────────

// writeTrialsCSV generates ~/.greenies/trials.csv from the current trial
// records. Only trials with at least one confirmed day are included — a trial
// with only observations and no confirmed parameters produces no CSV rows yet.
//
// Format: same sparse layout as crops.csv. The first row of each trial block
// includes all header fields (name, parameters, etc.). Subsequent rows for
// the same trial only fill in day, stage, and tasks — the rest is left empty
// so that a human reading the file in Google Sheets can see each trial block
// cleanly without repeating the header data on every line.
//
// The extra trial-specific columns (trial_variable, trial_start) come first,
// before the standard crop columns, so the file opens sensibly in a spreadsheet.
// countStages counts how many dark and light days are in a list of
// confirmed trial days. Used when writing CSV rows.
func countStages(days []TrialDayParams) (darkDays, lightDays int) {
	for _, d := range days {
		switch d.Stage {
		case "dark":
			darkDays++
		case "light":
			lightDays++
		}
	}
	return
}

// trialFieldStrings converts a trial record's numeric and boolean fields
// into CSV-ready strings. Empty string means "not recorded" — avoids
// misleading zeroes in the spreadsheet.
type trialFields struct {
	Soak, SoakHours, Seed, Medium, Yield string
	Dark, Light                          string
}

func formatTrialFields(tr TrialRecord) trialFields {
	f := trialFields{}
	f.Soak = "no"
	if tr.OvernightSoak {
		f.Soak = "yes"
	}
	if tr.SoakHours > 0 {
		f.SoakHours = strconv.FormatFloat(tr.SoakHours, 'f', -1, 64)
	}
	if tr.SeedGrams > 0 {
		f.Seed = strconv.FormatFloat(tr.SeedGrams, 'f', -1, 64)
	}
	if tr.MediumLitres > 0 {
		f.Medium = strconv.FormatFloat(tr.MediumLitres, 'f', -1, 64)
	}
	if tr.ActualYieldGrams > 0 {
		f.Yield = strconv.Itoa(tr.ActualYieldGrams)
	}
	dark, light := countStages(tr.ConfirmedDays)
	f.Dark = strconv.Itoa(dark)
	f.Light = strconv.Itoa(light)
	return f
}

func writeTrialsCSV(records []TrialRecord) error {
	csvPath, err := TrialsCSVPath()
	if err != nil {
		return err
	}

	f, err := os.Create(csvPath)
	if err != nil {
		return fmt.Errorf("could not create trials CSV: %w", err)
	}
	defer f.Close()

	w := csv.NewWriter(f)

	// Header row. Columns match the crops.csv format, with two
	// trial-specific columns (trial_variable, trial_start) added before
	// the standard crop columns so they are easy to spot in a spreadsheet.
	header := []string{
		"name", "trial_variable", "trial_start",
		"day", "stage", "tasks",
		"overnight_soak", "soak_hours", "seed_grams", "medium_litres",
		"dark_days", "light_days", "yield_grams",
	}
	if err := w.Write(header); err != nil {
		return err
	}

	for _, tr := range records {
		// Skip trials with no confirmed day data — nothing to write yet.
		if len(tr.ConfirmedDays) == 0 {
			continue
		}

		f := formatTrialFields(tr)

		// Write one CSV row per confirmed day.
		for i, d := range tr.ConfirmedDays {
			var row []string
			if i == 0 {
				// First row in this trial block: include all header-level fields.
				row = []string{
					tr.CropName, tr.TrialVariable, tr.SowDate,
					strconv.Itoa(d.Day), d.Stage, d.Tasks,
					f.Soak, f.SoakHours, f.Seed, f.Medium,
					f.Dark, f.Light, f.Yield,
				}
			} else {
				// Subsequent rows: sparse format — only day, stage, tasks.
				row = []string{
					tr.CropName, tr.TrialVariable, tr.SowDate,
					strconv.Itoa(d.Day), d.Stage, d.Tasks,
					"", "", "", "",
					"", "", "",
				}
			}
			if err := w.Write(row); err != nil {
				return err
			}
		}
	}

	w.Flush()
	return w.Error()
}

// ─────────────────────────────────────────────────────────────────────────────
// Promotion helper
// ─────────────────────────────────────────────────────────────────────────────

// ToCrop converts a trial record into a crop.Crop value — the same shape
// that crops.csv rows are read into. This is the bridge between the trial
// system and the main crop library: promoting a trial means turning its
// confirmed day-by-day parameters into an official Crop.
//
// A few fields need translating because trials record things slightly
// differently from the crop library:
//
//   - SoakHours and SeedGrams are decimals in a trial (the grower might have
//     tried 4.5 hours) but whole numbers in crops.csv, so they are rounded
//     to the nearest whole number here.
//   - ActualYieldGrams is the TOTAL weight the trial harvested, but the crop
//     library stores yield PER TRAY — so we divide by the tray count.
//   - MediumLitres defaults to 1 when the trial never recorded it, matching
//     the crop library's own default for a blank cell.
//   - Costing fields (seed cost, sell price) are not tracked during a trial,
//     so they start at zero — "not configured yet". The grower fills them in
//     later on the crop edit page.
func (tr TrialRecord) ToCrop() crop.Crop {
	darkDays, lightDays := countStages(tr.ConfirmedDays)

	// Convert each confirmed trial day into a crop library day row.
	days := make([]crop.CropDay, 0, len(tr.ConfirmedDays))
	for _, d := range tr.ConfirmedDays {
		days = append(days, crop.CropDay{
			Day:   d.Day,
			Stage: d.Stage,
			Tasks: d.Tasks,
		})
	}

	// Yield per tray = total harvested weight ÷ number of trays.
	// math.Round gives the nearest whole gram rather than always rounding down.
	yieldPerTray := 0
	if tr.Trays > 0 && tr.ActualYieldGrams > 0 {
		yieldPerTray = int(math.Round(float64(tr.ActualYieldGrams) / float64(tr.Trays)))
	}

	// The crop library treats a blank medium amount as 1 litre per tray.
	// A trial that never recorded it (zero) should get the same default.
	mediumLitres := tr.MediumLitres
	if mediumLitres <= 0 {
		mediumLitres = 1
	}

	return crop.Crop{
		Name:          tr.CropName,
		OvernightSoak: tr.OvernightSoak,
		SoakHours:     int(math.Round(tr.SoakHours)),
		SeedGrams:     int(math.Round(tr.SeedGrams)),
		MediumLitres:  mediumLitres,
		DarkDays:      darkDays,
		LightDays:     lightDays,
		YieldGrams:    yieldPerTray,
		Days:          days,
	}
}

// AppendToCropsCSV appends the confirmed parameters from a promoted trial to
// the user's crops.csv file. This is what "promoting" a trial means: the
// rows confirmed day by day through the manage flow become official crop
// parameters that any future "greenies plan" can use.
//
// The function takes the path to crops.csv so it can be tested without
// touching the real file. In practice, the CLI and GUI pass the user's real path.
//
// How it works: load every crop already in the file, add the promoted trial
// to the end of that list, and write the whole file back out with the
// standard crop.WriteCrops writer. Because that writer places every value
// under its named column header, the promoted rows can never drift out of
// line with the file's column layout. (Writing cells by position instead of
// by name is the failure mode promote_test.go guards against.)
//
// If the same crop name already exists, the promoted trial is added as a
// second entry alongside it — intentional, and the promote flows warn the
// grower first. One entry might be the old seed lot, the other the new one.
func AppendToCropsCSV(cropsPath string, tr TrialRecord) error {
	// Load whatever is already in crops.csv so we can add to it rather than
	// replace it. A missing file just means an empty library (first run).
	var existing []crop.Crop
	if _, statErr := os.Stat(cropsPath); statErr == nil {
		loaded, loadErr := (crop.CSVSource{Path: cropsPath}).LoadCrops()
		if loadErr != nil {
			// The file exists but can't be read. Stop here rather than write
			// over it — overwriting would destroy whatever the grower has.
			return fmt.Errorf("could not read the existing crop library: %w", loadErr)
		}
		existing = loaded
	}

	existing = append(existing, tr.ToCrop())
	return crop.WriteCrops(cropsPath, existing)
}

// ─────────────────────────────────────────────────────────────────────────────
// Query helpers
// ─────────────────────────────────────────────────────────────────────────────

// ActiveTrials returns only the trials currently in the "active" state.
// Used by the snapshot and the manage flow to filter the full list.
func ActiveTrials(records []TrialRecord) []TrialRecord {
	var active []TrialRecord
	for _, tr := range records {
		if tr.Status == StatusActive {
			active = append(active, tr)
		}
	}
	return active
}

// PastTrialsByName returns all non-active trials whose CropName matches the
// given name (case-insensitive). Used to show historical context before
// starting a new trial of the same crop.
func PastTrialsByName(records []TrialRecord, cropName string) []TrialRecord {
	lower := strings.ToLower(cropName)
	var past []TrialRecord
	for _, tr := range records {
		if strings.ToLower(tr.CropName) == lower && tr.Status != StatusActive {
			past = append(past, tr)
		}
	}
	return past
}

// FindByID returns a pointer to the trial with the given ID in the slice,
// or nil if no match is found. The pointer refers directly into the slice,
// so changes to the returned record will modify the original.
func FindByID(records []TrialRecord, id string) *TrialRecord {
	for i := range records {
		if records[i].ID == id {
			return &records[i]
		}
	}
	return nil
}

// ReplaceByID returns a new slice with the trial matching id replaced by
// updated. If no match is found, updated is appended as a new entry.
// This is the standard way to save changes to a single trial record without
// having to rewrite the whole list-management logic everywhere.
func ReplaceByID(records []TrialRecord, updated TrialRecord) []TrialRecord {
	for i, tr := range records {
		if tr.ID == updated.ID {
			records[i] = updated
			return records
		}
	}
	// Not found — this is a new trial being added for the first time.
	return append(records, updated)
}

// RemoveByID returns a new slice with the trial matching id removed.
// Used when the grower discards a trial and wants all data deleted.
func RemoveByID(records []TrialRecord, id string) []TrialRecord {
	var result []TrialRecord
	for _, tr := range records {
		if tr.ID != id {
			result = append(result, tr)
		}
	}
	return result
}
