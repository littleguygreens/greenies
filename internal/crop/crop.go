// Package crop defines what a crop variety looks like, and where crop data
// comes from.
//
// Think of this package as the crop library section of the farm. It describes
// each variety (sunnies, peas, daikon, etc.) along with all the daily tasks
// required to grow it from sow to harvest.
//
// The CropSource interface at the bottom of this file is the "pathway" that
// lets us swap out where crop data comes from — a local CSV file today,
// Google Sheets in a future phase — without changing any of the scheduling
// code that uses it.
package crop

// CropDay represents one day in a crop's growing cycle.
// It maps directly to one row in the crops.csv file.
type CropDay struct {
	// Day is the day number in the cycle. Day 1 is always the sowing day.
	// Some crops have a Day 0 for an overnight soak — that is also a CropDay.
	Day int

	// Stage describes which phase of growth this day falls in.
	// Valid values are: "sow", "dark", "light", "harvest".
	Stage string

	// Tasks is the free-form description of what needs doing on this day.
	// Copied directly from the CSV and printed on the calendar as-is.
	// Can be empty — some light-stage days require no action.
	Tasks string
}

// Crop holds everything the program needs to know about one crop variety.
//
// The parameters (seed weight, cycle length, etc.) come from the first row
// of that crop's block in the CSV. The Days slice holds all the daily tasks
// for the full cycle, in order.
type Crop struct {
	// Name is the crop's display name as written in the CSV, e.g. "sunnies".
	Name string

	// CycleDays is the total length of the grow cycle. The harvest always
	// falls on this day number. Used to calculate sow date from harvest date.
	// Example: sunnies = 9, peas = 8.
	CycleDays int

	// OvernightSoak is true if this crop requires a seed soak the night
	// before sowing (Day 0). When true, the CSV will have a Day 0 row.
	OvernightSoak bool

	// SoakHours is how many hours the seed should soak.
	// For overnight soaks (peas, daikon) this is typically 12.
	// For same-day soaks (sunnies) it might be 4.
	SoakHours int

	// SeedGrams is how many grams of seed go into one tray.
	// Multiply by tray count to get the total seed needed for a batch.
	SeedGrams int

	// DirtLitres is how many litres of growing medium go into one tray.
	// Defaults to 1 if not specified in the CSV.
	DirtLitres float64

	// DarkDays is how many days the crop spends in the dark room.
	// (Dark and blackout refer to the same stage.)
	DarkDays int

	// LightDays is how many days the crop spends on the lit rack before harvest.
	LightDays int

	// YieldGrams is the expected harvest weight from one tray.
	// Multiply by tray count to estimate total yield for a batch.
	YieldGrams int

	// Days is the ordered list of daily entries for this crop, from Day 0 or
	// Day 1 through to the harvest day. Each entry maps to one calendar task.
	Days []CropDay
}

// CropSource is an interface — a promise that whatever implements it can
// hand back a complete list of crop varieties.
//
// Think of it like a job description: "to fill this role, you must be able
// to load crops and return them." Right now the only thing filling this role
// is the CSV reader in csv.go. In a future phase, a Google Sheets reader
// will fill the same role, and the rest of the program won't need to change
// at all — it will just ask "give me the crops" and get the same answer
// regardless of where they came from.
type CropSource interface {
	// LoadCrops reads all crop varieties and returns them as a slice.
	// Returns an error if the data cannot be read or is malformed.
	LoadCrops() ([]Crop, error)
}
