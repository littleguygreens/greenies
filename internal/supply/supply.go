// Package supply manages farm-wide supply costs — things like labels,
// containers, and grow medium that are the same price no matter which crop
// you are growing.
//
// These costs are stored in a simple CSV file (~/.greenies/supplies.csv)
// with four columns: item name, category, cost per case/bag, and units per
// case/bag. The program divides to get the per-unit cost automatically.
//
// Each supply has a category that tells the program what role it plays:
//   - "medium"    — the growing medium (e.g. coco coir, peat moss)
//   - "container" — the food-service container each unit is sold in
//   - "label"     — the label stuck on each container
//
// The category is what the profitability calculator uses to find the right
// cost — not the name. This means the grower can rename "grow medium" to
// "coco coir" or "containers" to "clamshells" without breaking anything.
//
// This data is kept separate from crops.csv (which holds per-crop parameters)
// and farm.csv (which holds physical spaces and inventory counts). Together,
// supply costs + crop parameters let the program calculate full per-tray and
// per-unit profitability.
package supply

import (
	"encoding/csv" // Go's built-in CSV parser
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"

	"github.com/littleguygreens/greenies/internal/crop"
)

// Supply represents one line item in the supplies file — a consumable that
// the farm buys in bulk (cases, bags, etc.) and uses across all crops.
type Supply struct {
	// Name identifies the supply item, e.g. "labels", "containers", "grow medium".
	Name string

	// Category tells the profitability calculator what role this supply plays.
	// Valid values: "medium", "container", "label", or "" (no special role).
	// The calculator looks up supplies by category, not by name — so the
	// grower can rename items without breaking cost calculations.
	Category string

	// CostPerCase is the purchase price of one case/bag, in dollars.
	// Example: $12.50 for a case of 500 labels.
	CostPerCase float64

	// UnitsPerCase is how many individual items come in one case/bag.
	// For labels this is labels per case; for medium this is litres per bag.
	UnitsPerCase float64
}

// CostPerUnit returns the cost of a single item (one label, one container,
// one litre of grow medium). It divides the bulk purchase price by the
// number of items in the package. Returns 0 if UnitsPerCase is zero
// (avoids a division-by-zero crash).
func (s Supply) CostPerUnit() float64 {
	if s.UnitsPerCase == 0 {
		return 0
	}
	return s.CostPerCase / s.UnitsPerCase
}

// ─── File path ──────────────────────────────────────────────────────────────

// FilePath returns the full path to the supplies CSV file.
// It lives in ~/.greenies/ alongside crops.csv and farm.csv.
func FilePath() (string, error) {
	home, err := os.UserHomeDir()
	if err != nil {
		return "", fmt.Errorf("could not find your home directory: %w", err)
	}
	return filepath.Join(home, ".greenies", "supplies.csv"), nil
}

// ─── Load ───────────────────────────────────────────────────────────────────

// Load reads the supplies CSV file and returns all supply items.
// Returns an empty slice (not an error) if the file does not exist yet —
// this handles first-time setup gracefully before the grower has created
// the file.
func Load() ([]Supply, error) {
	path, err := FilePath()
	if err != nil {
		return nil, err
	}

	f, err := os.Open(path)
	if err != nil {
		if os.IsNotExist(err) {
			return nil, nil // file doesn't exist yet — that's fine
		}
		return nil, fmt.Errorf("could not open supplies file: %w", err)
	}
	defer f.Close()

	reader := csv.NewReader(f)
	reader.FieldsPerRecord = -1 // allow short rows

	rows, err := reader.ReadAll()
	if err != nil {
		return nil, fmt.Errorf("could not read supplies file: %w", err)
	}

	if len(rows) < 2 {
		return nil, nil // header only, no data
	}

	// Build a column-name → index map from the header row.
	// Same pattern used throughout the project — columns are found by
	// name, not by position, so reordering in a spreadsheet is safe.
	header := rows[0]
	col := make(map[string]int, len(header))
	for i, name := range header {
		col[strings.TrimSpace(strings.ToLower(name))] = i
	}

	// get safely retrieves a cell value by column name.
	get := func(row []string, name string) string {
		i, ok := col[name]
		if !ok || i >= len(row) {
			return ""
		}
		return strings.TrimSpace(row[i])
	}

	var supplies []Supply
	for _, row := range rows[1:] {
		name := get(row, "name")
		if name == "" {
			continue
		}

		costStr := get(row, "cost_per_case")
		cost, _ := strconv.ParseFloat(costStr, 64)

		unitsStr := get(row, "units_per_case")
		units, _ := strconv.ParseFloat(unitsStr, 64)

		supplies = append(supplies, Supply{
			Name:         name,
			Category:     get(row, "category"),
			CostPerCase:  cost,
			UnitsPerCase: units,
		})
	}

	return supplies, nil
}

// ─── Save ───────────────────────────────────────────────────────────────────

// Save writes the given supplies to the CSV file, completely replacing its
// contents. Uses the same name-based column approach as the rest of the
// project.
func Save(supplies []Supply) error {
	path, err := FilePath()
	if err != nil {
		return err
	}

	out, err := os.Create(path)
	if err != nil {
		return fmt.Errorf("could not create supplies file: %w", err)
	}
	defer out.Close()

	w := csv.NewWriter(out)

	header := []string{"name", "category", "cost_per_case", "units_per_case"}
	col := make(map[string]int, len(header))
	for i, name := range header {
		col[name] = i
	}

	if err := w.Write(header); err != nil {
		return fmt.Errorf("could not write header: %w", err)
	}

	for _, s := range supplies {
		row := make([]string, len(header))
		row[col["name"]] = s.Name
		row[col["category"]] = s.Category
		row[col["cost_per_case"]] = crop.FormatFloat(s.CostPerCase)
		row[col["units_per_case"]] = crop.FormatFloat(s.UnitsPerCase)

		if err := w.Write(row); err != nil {
			return fmt.Errorf("could not write row for %s: %w", s.Name, err)
		}
	}

	w.Flush()
	return w.Error()
}


// ─── Lookup helpers ─────────────────────────────────────────────────────────

// FindByCategory returns the first supply item with the given category
// (case-insensitive), or nil if not found. The profitability calculator
// uses this to look up supplies by role ("medium", "container", "label")
// regardless of what the grower has named them.
func FindByCategory(supplies []Supply, category string) *Supply {
	for i := range supplies {
		if strings.EqualFold(supplies[i].Category, category) {
			return &supplies[i]
		}
	}
	return nil
}

// LoadSupplyCosts is a convenience function that loads the supplies CSV and
// builds a crop.SupplyCosts struct in one step. It looks up the three
// farm-wide consumables by their category — "medium", "container", and
// "label" — and calculates their per-unit costs.
//
// If the supplies file doesn't exist or can't be read, the returned struct
// has all zeroes — the profitability calculations still work, they just
// won't include supply costs.
func LoadSupplyCosts() crop.SupplyCosts {
	supplies, _ := Load()
	sc := crop.SupplyCosts{}
	if s := FindByCategory(supplies, "medium"); s != nil {
		sc.MediumPerLitre = s.CostPerUnit()
	}
	if s := FindByCategory(supplies, "container"); s != nil {
		sc.ContainerEach = s.CostPerUnit()
	}
	if s := FindByCategory(supplies, "label"); s != nil {
		sc.LabelEach = s.CostPerUnit()
	}
	return sc
}
