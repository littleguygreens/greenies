// Package farm defines the physical layout of the farm and tracks what is
// currently growing on it.
//
// Think of this package as the farm's filing system:
//   - The layout config (farm.csv) describes the physical spaces — how many
//     slots are in the blackout room, main tent, and test tent.
//   - The cycle records (cycles.json) remember every batch that has been
//     planned: what crop, how many trays, which dates, and where it's headed.
//
// Together these two data sources power the "greenies snapshot" command, which
// shows a live picture of the farm without the user having to type anything.
package farm

import (
	"encoding/csv"   // Go's built-in CSV reader and writer
	"encoding/json"  // Go's built-in JSON reader and writer
	"fmt"
	"os"             // for reading, writing, and checking files
	"path/filepath"  // for building file paths that work on any operating system
	"strconv"        // for converting text like "64" into the number 64
	"strings"        // for string utilities like replacing underscores with spaces
)

// ─────────────────────────────────────────────────────────────────────────────
// Data types
// ─────────────────────────────────────────────────────────────────────────────

// Environment describes one physical space on the farm — the blackout room,
// the main tent, or the test tent.
//
// The Capacity field stores the total number of tray slots available in that
// space. Right now we just store one number, but the struct is designed to
// grow: a future version might add individual rack and shelf details without
// breaking anything that already uses this type.
type Environment struct {
	// Name is the short identifier used in farm.csv and cycles.json.
	// Uses underscores rather than spaces so it is easy to type and match.
	// Examples: "blackout", "main_tent", "test_tent".
	Name string

	// Type is either "blackout" (the shared dark room) or "lit" (a lit tent).
	// The snapshot uses this to decide which section each environment belongs to.
	Type string

	// Capacity is the total number of tray slots in this environment.
	// One grow tray = one slot, regardless of stacking arrangement.
	Capacity int
}

// Cycle is a record of one planned crop batch — one run of "greenies plan".
//
// When the grower plans a crop, all the daily tasks are saved to the calendar
// (tasks.json). A Cycle record is also saved here (cycles.json) as a compact
// summary. The snapshot uses the summary dates to figure out where each batch
// is right now without having to read every individual task.
//
// Each weekly repeat within a single planning session gets its own separate
// Cycle record with its own CycleID, so you can track and delete weeks
// independently.
type Cycle struct {
	// CycleID ties this record to the calendar tasks that were created at
	// the same time. Every task in the same planning run shares this ID,
	// so deleting a cycle from the calendar also removes the right tasks.
	CycleID string

	// CropName is the variety name exactly as it appears in crops.csv.
	// Example: "sunnies", "peas", "daikon".
	CropName string

	// Trays is how many grow trays were planned in this batch.
	Trays int

	// SowDate is the date the seed was (or will be) sown — always Day 1 of
	// the cycle. For crops with an overnight soak (Day 0), SowDate is the
	// day after the soak, when the trays are actually prepared and filled.
	// Format: YYYY-MM-DD.
	SowDate string

	// HarvestDate is the last day of the cycle. Format: YYYY-MM-DD.
	HarvestDate string

	// MoveToLightDate is the first day the trays spend on a lit rack.
	// Before this date, the trays are in the blackout room.
	// On and after this date, they are in the assigned lit environment.
	// Format: YYYY-MM-DD.
	//
	// This is computed once at plan time (sow date + DarkDays + 1) so the
	// snapshot never needs to re-read the crop library to figure it out.
	MoveToLightDate string

	// LitEnvironment is the name of the lit space this batch is headed to
	// once it leaves the blackout room. Matches the Name field of an
	// Environment in farm.csv — for example "main_tent" or "test_tent".
	//
	// The special value "either" means the grower hadn't decided at plan
	// time. The snapshot resolves "either" at display time by assigning the
	// batch to the first lit environment that has available capacity.
	LitEnvironment string

	// ExpectedGrams is the estimated harvest weight for this batch in grams,
	// computed at plan time as: yield_grams_per_tray × tray_count.
	//
	// Storing it here (rather than re-reading the crop library at harvest time)
	// means the harvest log always has the number that was promised when the
	// plan was made — even if the crop library is later edited.
	//
	// A value of 0 means no yield data was available when this cycle was
	// planned (e.g. cycles saved before this field was added). The harvest
	// log treats 0 as "unknown" and omits the expected/actual comparison.
	ExpectedGrams int
}

// ─────────────────────────────────────────────────────────────────────────────
// Harvest records
// ─────────────────────────────────────────────────────────────────────────────

// HarvestRecord is a single logged harvest — what the plan promised vs what
// the grower actually cut on the day.
//
// One record is saved per crop batch (per Cycle). A batch of 3 trays of sunnies
// on the same harvest day is one record, not three — because it was planned as
// one unit and harvested as one unit.
type HarvestRecord struct {
	// CycleID links this record back to the matching Cycle in cycles.json.
	// Used to prevent the same cycle being logged twice.
	CycleID string

	// CropName is the variety name, e.g. "sunnies". Copied from the Cycle
	// so the harvest log can be read without also loading cycles.json.
	CropName string

	// HarvestDate is the date the crop was actually cut. Format: YYYY-MM-DD.
	HarvestDate string

	// ExpectedTrays is how many trays were planned (from the Cycle record).
	// Compared against ActualTrays in the harvest log.
	ExpectedTrays int

	// ActualTrays is how many trays the grower actually cut on harvest day.
	// May differ from ExpectedTrays if a tray was lost or discarded.
	ActualTrays int

	// ExpectedGrams is the estimated yield at plan time (copied from the
	// Cycle's ExpectedGrams field). Zero means unknown.
	ExpectedGrams int

	// ActualGrams is the total weight of the harvest in grams, as measured
	// by the grower on harvest day.
	ActualGrams int

	// Notes is an optional free-text comment the grower enters at log time.
	// Useful for recording anything unusual: "hot week — small leaves",
	// "lost one tray to mould", etc.
	Notes string
}

// ─────────────────────────────────────────────────────────────────────────────
// File paths
// ─────────────────────────────────────────────────────────────────────────────

// dataDir returns the path to the shared data folder for all greenies files.
// On Linux this is typically /home/<username>/.greenies.
//
// Using os.UserHomeDir() instead of a hardcoded path means the program works
// correctly on any machine and for any user — a requirement for open-source.
func dataDir() (string, error) {
	home, err := os.UserHomeDir()
	if err != nil {
		return "", fmt.Errorf("could not find home directory: %w", err)
	}
	return filepath.Join(home, ".greenies"), nil
}

// FarmConfigPath returns the full path to the farm layout CSV file.
// Example: /home/farm/.greenies/farm.csv
func FarmConfigPath() (string, error) {
	dir, err := dataDir()
	if err != nil {
		return "", err
	}
	return filepath.Join(dir, "farm.csv"), nil
}

// CyclesFilePath returns the full path to the cycle records JSON file.
// Example: /home/farm/.greenies/cycles.json
func CyclesFilePath() (string, error) {
	dir, err := dataDir()
	if err != nil {
		return "", err
	}
	return filepath.Join(dir, "cycles.json"), nil
}

// ─────────────────────────────────────────────────────────────────────────────
// Farm layout config (farm.csv)
// ─────────────────────────────────────────────────────────────────────────────

// defaultConfig returns the built-in farm layout used when farm.csv doesn't
// exist yet. The file is created automatically from these defaults on first run.
//
// The rows fall into three types (stored in the "type" column of farm.csv):
//
//	"blackout"  — the shared dark room where trays germinate
//	"lit"       — a tent or rack that provides light
//	"inventory" — a physical item with a fixed stock count (e.g. grow trays)
//
// Inventory rows use the Capacity field to store the total number of that item
// the farm owns. The snapshot computes how many are in use from the cycle
// records and shows how many are available in the cupboard.
func defaultConfig() []Environment {
	return []Environment{
		// ── Physical spaces ──────────────────────────────────────────────────
		{Name: "blackout", Type: "blackout", Capacity: 100},
		{Name: "main_tent", Type: "lit", Capacity: 64},
		{Name: "test_tent", Type: "lit", Capacity: 16},

		// ── Physical tray stock ──────────────────────────────────────────────
		// Grow trays hold the crop for its entire life from sowing to harvest.
		// Bottom trays pair with grow trays during germination and blackout only
		// — they are returned to the cupboard the moment trays move to light.
		// Edit these numbers in ~/.greenies/farm.csv to match your actual stock.
		{Name: "grow_trays", Type: "inventory", Capacity: 150},
		{Name: "bottom_trays", Type: "inventory", Capacity: 150},
	}
}

// LoadConfig reads the farm layout from ~/.greenies/farm.csv and returns a
// list of environments.
//
// If farm.csv doesn't exist yet, LoadConfig automatically creates it with the
// default layout (blackout room, main tent, test tent) so the program works
// right out of the box without any manual setup.
func LoadConfig() ([]Environment, error) {
	path, err := FarmConfigPath()
	if err != nil {
		return nil, err
	}

	// If the file doesn't exist, create it from the defaults and return those.
	// os.Stat checks whether a file exists; os.IsNotExist converts the error
	// into a simple yes/no answer.
	if _, statErr := os.Stat(path); os.IsNotExist(statErr) {
		defaults := defaultConfig()
		// Try to write the default file, but don't fail if we can't — the
		// program can still run using the in-memory defaults.
		_ = WriteConfig(path, defaults)
		return defaults, nil
	}

	f, err := os.Open(path)
	if err != nil {
		return nil, fmt.Errorf("could not open farm config at %s: %w", path, err)
	}
	defer f.Close()

	reader := csv.NewReader(f)
	// Allow rows to have fewer fields than the header, in case a spreadsheet
	// app omits trailing empty cells.
	reader.FieldsPerRecord = -1

	rows, err := reader.ReadAll()
	if err != nil {
		return nil, fmt.Errorf("could not read farm config: %w", err)
	}

	if len(rows) < 2 {
		return nil, fmt.Errorf("farm.csv appears to be empty — add at least one environment row")
	}

	// Build a map from column name → column index so reordering columns in
	// the spreadsheet doesn't break anything.
	header := rows[0]
	col := make(map[string]int, len(header))
	for i, name := range header {
		col[strings.TrimSpace(name)] = i
	}

	// get safely retrieves a cell by column name from a row.
	get := func(row []string, name string) string {
		i, ok := col[name]
		if !ok || i >= len(row) {
			return ""
		}
		return strings.TrimSpace(row[i])
	}

	var envs []Environment
	for lineNum, row := range rows[1:] {
		name := get(row, "name")
		if name == "" {
			continue // skip blank rows
		}

		capStr := get(row, "capacity")
		capacity := 0
		if capStr != "" {
			n, err := strconv.Atoi(capStr)
			if err != nil {
				return nil, fmt.Errorf("farm.csv line %d: capacity %q is not a whole number", lineNum+2, capStr)
			}
			capacity = n
		}

		envs = append(envs, Environment{
			Name:     name,
			Type:     get(row, "type"),
			Capacity: capacity,
		})
	}

	if len(envs) == 0 {
		return nil, fmt.Errorf("farm.csv has no environment rows — check the file")
	}

	return envs, nil
}

// WriteConfig writes a list of environments to a CSV file at the given path.
// Used to create the default farm.csv on first run, and to save farm data
// pulled from Google Sheets during sync.
func WriteConfig(path string, envs []Environment) error {
	// Make sure the ~/.greenies directory exists before trying to write.
	if err := os.MkdirAll(filepath.Dir(path), 0755); err != nil {
		return fmt.Errorf("could not create data directory: %w", err)
	}

	f, err := os.Create(path)
	if err != nil {
		return fmt.Errorf("could not create farm config file: %w", err)
	}
	defer f.Close()

	w := csv.NewWriter(f)
	// Write the header row.
	if err := w.Write([]string{"name", "type", "capacity"}); err != nil {
		return err
	}
	// Write one row per environment.
	for _, env := range envs {
		if err := w.Write([]string{env.Name, env.Type, strconv.Itoa(env.Capacity)}); err != nil {
			return err
		}
	}
	w.Flush()
	return w.Error()
}

// DisplayName formats an environment name for human reading.
// Underscores become spaces and the first letter is capitalised.
// This is used in prompts and the snapshot display.
//
// Examples:
//
//	"blackout"  → "Blackout"
//	"main_tent" → "Main tent"
//	"test_tent" → "Test tent"
func DisplayName(name string) string {
	s := strings.ReplaceAll(name, "_", " ")
	if s == "" {
		return ""
	}
	return strings.ToUpper(s[:1]) + s[1:]
}

// ─────────────────────────────────────────────────────────────────────────────
// Cycle records (cycles.json)
// ─────────────────────────────────────────────────────────────────────────────

// LoadCycles reads all saved cycle records from disk and returns them as a list.
//
// If cycles.json doesn't exist yet (e.g. on first run, or before any cycles
// have been planned), LoadCycles returns an empty list rather than an error —
// the same friendly behaviour as store.Load() for tasks.
func LoadCycles() ([]Cycle, error) {
	path, err := CyclesFilePath()
	if err != nil {
		return nil, err
	}

	data, err := os.ReadFile(path)
	if err != nil {
		if os.IsNotExist(err) {
			return []Cycle{}, nil
		}
		return nil, fmt.Errorf("could not read cycles file: %w", err)
	}

	var cycles []Cycle
	if err := json.Unmarshal(data, &cycles); err != nil {
		return nil, fmt.Errorf("cycles file appears to be corrupted: %w", err)
	}

	return cycles, nil
}

// HarvestsFilePath returns the full path to the harvest log JSON file.
// Example: /home/farm/.greenies/harvests.json
func HarvestsFilePath() (string, error) {
	dir, err := dataDir()
	if err != nil {
		return "", err
	}
	return filepath.Join(dir, "harvests.json"), nil
}

// LoadHarvests reads all saved harvest records from disk and returns them as a
// list. If the file doesn't exist yet (no harvests have been logged), it
// returns an empty list rather than an error — the same friendly behaviour as
// LoadCycles for new installs.
func LoadHarvests() ([]HarvestRecord, error) {
	path, err := HarvestsFilePath()
	if err != nil {
		return nil, err
	}

	data, err := os.ReadFile(path)
	if err != nil {
		if os.IsNotExist(err) {
			return []HarvestRecord{}, nil
		}
		return nil, fmt.Errorf("could not read harvest log: %w", err)
	}

	var records []HarvestRecord
	if err := json.Unmarshal(data, &records); err != nil {
		return nil, fmt.Errorf("harvest log appears to be corrupted: %w", err)
	}

	return records, nil
}

// SaveHarvests writes the full list of harvest records to disk, replacing
// whatever was there before.
func SaveHarvests(records []HarvestRecord) error {
	dir, err := dataDir()
	if err != nil {
		return err
	}

	if err := os.MkdirAll(dir, 0755); err != nil {
		return fmt.Errorf("could not create data directory: %w", err)
	}

	path, err := HarvestsFilePath()
	if err != nil {
		return err
	}

	data, err := json.MarshalIndent(records, "", "  ")
	if err != nil {
		return fmt.Errorf("could not encode harvest records: %w", err)
	}

	if err := os.WriteFile(path, data, 0644); err != nil {
		return fmt.Errorf("could not write harvest log: %w", err)
	}

	return nil
}

// SaveCycles writes the full list of cycle records to disk, replacing whatever
// was there before. This is a complete overwrite every time — simple and safe
// for the number of records a single farm will ever have.
func SaveCycles(cycles []Cycle) error {
	dir, err := dataDir()
	if err != nil {
		return err
	}

	if err := os.MkdirAll(dir, 0755); err != nil {
		return fmt.Errorf("could not create data directory: %w", err)
	}

	path, err := CyclesFilePath()
	if err != nil {
		return err
	}

	// json.MarshalIndent adds line breaks and indentation so the file is
	// readable if someone opens it in a text editor.
	data, err := json.MarshalIndent(cycles, "", "  ")
	if err != nil {
		return fmt.Errorf("could not encode cycles: %w", err)
	}

	if err := os.WriteFile(path, data, 0644); err != nil {
		return fmt.Errorf("could not write cycles file: %w", err)
	}

	return nil
}
