// profit.go — derived profitability calculations for a crop variety.
//
// None of these numbers are ever stored. They are calculated on the fly from:
//  1. The crop's own parameters (SeedCost, SeedGrams, etc.)
//  2. Farm-wide supply costs (medium, containers, labels) passed in by the caller.
//
// This keeps the crop package independent of the supply package — the caller
// loads supplies and passes the relevant costs in via a SupplyCosts struct.
package crop

import "math"

// SupplyCosts holds the per-unit cost of farm-wide consumables. The caller
// fills this in by looking up items in the supplies.csv data. If a supply
// is not configured, its cost will be zero — the calculations still work,
// they just won't include that item.
type SupplyCosts struct {
	// MediumPerLitre is the cost of one litre of growing medium, in dollars.
	MediumPerLitre float64

	// ContainerEach is the cost of one food-service container (clamshell),
	// in dollars.
	ContainerEach float64

	// LabelEach is the cost of one label, in dollars.
	LabelEach float64
}

// ── Per-tray cost breakdown ─────────────────────────────────────────────────

// SeedCostPerTray returns how much seed costs for one tray, in dollars.
// Formula: (SeedCost / SeedPurchaseWeight) × SeedGrams.
// Returns 0 if the grower hasn't entered seed purchase info yet.
func (c Crop) SeedCostPerTray() float64 {
	if c.SeedPurchaseWeight == 0 || c.SeedCost == 0 {
		return 0
	}
	return (c.SeedCost / c.SeedPurchaseWeight) * float64(c.SeedGrams)
}

// MediumCostPerTray returns how much growing medium costs for one tray.
// Formula: MediumLitres × MediumPerLitre.
func (c Crop) MediumCostPerTray(sc SupplyCosts) float64 {
	return c.MediumLitres * sc.MediumPerLitre
}

// PackagingCostPerTray returns the total packaging cost (containers + labels)
// for one tray's worth of harvest. Uses the exact (unrounded) UnitsPerTray
// because this is a per-tray estimate — rounding happens at the cycle level
// when calculating actual sellable units across multiple trays.
func (c Crop) PackagingCostPerTray(sc SupplyCosts) float64 {
	return c.UnitsPerTray() * (sc.ContainerEach + sc.LabelEach)
}

// TotalCostPerTray returns the all-in cost to grow and package one tray.
// This is the sum of seed + medium + packaging costs.
func (c Crop) TotalCostPerTray(sc SupplyCosts) float64 {
	return c.SeedCostPerTray() + c.MediumCostPerTray(sc) + c.PackagingCostPerTray(sc)
}

// ── Revenue and profit ──────────────────────────────────────────────────────

// UnitsPerTray returns how many sellable units one tray produces as an exact
// decimal. For example, 300 g yield / 113 g per unit = 2.6549...
// The caller decides when to round: the crop library table shows the exact
// number, but financial projections for a whole cycle use SellableUnits()
// which rounds down (you can't sell a partial clamshell).
// Returns 0 if yield or unit weight is not set.
func (c Crop) UnitsPerTray() float64 {
	if c.UnitWeight == 0 || c.YieldGrams == 0 {
		return 0
	}
	return float64(c.YieldGrams) / c.UnitWeight
}

// SellableUnits returns the total number of whole sellable units from a
// batch of trays. This is the number you can actually package and sell —
// fractional units are thrown away (you can't sell 0.65 of a clamshell).
// Formula: floor(UnitsPerTray × trays).
func (c Crop) SellableUnits(trays int) int {
	return int(math.Floor(c.UnitsPerTray() * float64(trays)))
}

// RevenuePerTray returns the total revenue from selling one tray's harvest.
// Uses exact UnitsPerTray — this is a per-tray estimate. For whole-cycle
// revenue with rounding, use SellableUnits() × UnitSellPrice.
func (c Crop) RevenuePerTray() float64 {
	return c.UnitsPerTray() * c.UnitSellPrice
}

// ProfitPerTray returns the dollar profit from one tray (revenue minus cost).
func (c Crop) ProfitPerTray(sc SupplyCosts) float64 {
	return c.RevenuePerTray() - c.TotalCostPerTray(sc)
}

// ProfitMargin returns the profit as a percentage of revenue.
// Returns 0 if revenue is zero (avoids division by zero).
func (c Crop) ProfitMargin(sc SupplyCosts) float64 {
	rev := c.RevenuePerTray()
	if rev == 0 {
		return 0
	}
	return (c.ProfitPerTray(sc) / rev) * 100
}

// ── Per-unit numbers ────────────────────────────────────────────────────────

// CostPerUnit returns the all-in cost to produce one sellable unit.
// Formula: TotalCostPerTray / UnitsPerTray.
func (c Crop) CostPerUnit(sc SupplyCosts) float64 {
	units := c.UnitsPerTray()
	if units == 0 {
		return 0
	}
	return c.TotalCostPerTray(sc) / units
}

// ProfitPerUnit returns the dollar profit per sellable unit.
// Formula: UnitSellPrice − CostPerUnit.
func (c Crop) ProfitPerUnit(sc SupplyCosts) float64 {
	return c.UnitSellPrice - c.CostPerUnit(sc)
}

// ── Completeness check ──────────────────────────────────────────────────────

// HasCostingData returns true if the grower has entered enough data to
// calculate meaningful profitability numbers. "Enough" means: seed cost info
// is filled in AND a sell price is set. Supply costs are optional extras.
func (c Crop) HasCostingData() bool {
	return c.SeedCost > 0 && c.SeedPurchaseWeight > 0 && c.UnitSellPrice > 0
}

// ── Formatting helpers ──────────────────────────────────────────────────────

// RoundCents rounds a dollar amount to 2 decimal places.
// Used for display — never for further calculations.
func RoundCents(v float64) float64 {
	return math.Round(v*100) / 100
}
