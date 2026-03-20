// profit.go — derived profitability calculations for a crop variety.
//
// None of these numbers are ever stored. They are calculated on the fly from:
//   1. The crop's own parameters (SeedCost, SeedGrams, etc.)
//   2. Farm-wide supply costs (dirt, containers, labels) passed in by the caller.
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
	// DirtPerLitre is the cost of one litre of growing medium, in dollars.
	DirtPerLitre float64

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
	return (c.SeedCost / float64(c.SeedPurchaseWeight)) * float64(c.SeedGrams)
}

// DirtCostPerTray returns how much growing medium costs for one tray.
// Formula: DirtLitres × dirtPerLitre.
func (c Crop) DirtCostPerTray(sc SupplyCosts) float64 {
	return c.DirtLitres * sc.DirtPerLitre
}

// PackagingCostPerTray returns the total packaging cost (containers + labels)
// for one tray's worth of harvest.
// Formula: UnitsPerTray × (containerEach + labelEach).
func (c Crop) PackagingCostPerTray(sc SupplyCosts) float64 {
	units := c.UnitsPerTray()
	return float64(units) * (sc.ContainerEach + sc.LabelEach)
}

// TotalCostPerTray returns the all-in cost to grow and package one tray.
// This is the sum of seed + dirt + packaging costs.
func (c Crop) TotalCostPerTray(sc SupplyCosts) float64 {
	return c.SeedCostPerTray() + c.DirtCostPerTray(sc) + c.PackagingCostPerTray(sc)
}

// ── Revenue and profit ──────────────────────────────────────────────────────

// UnitsPerTray returns how many sellable units one tray produces.
// Formula: YieldGrams / UnitWeight, rounded down (you can't sell a partial unit).
// Returns 0 if yield or unit weight is not set.
func (c Crop) UnitsPerTray() int {
	if c.UnitWeight == 0 || c.YieldGrams == 0 {
		return 0
	}
	return c.YieldGrams / c.UnitWeight
}

// RevenuePerTray returns the total revenue from selling one tray's harvest.
// Formula: UnitsPerTray × UnitSellPrice.
func (c Crop) RevenuePerTray() float64 {
	return float64(c.UnitsPerTray()) * c.UnitSellPrice
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
	return c.TotalCostPerTray(sc) / float64(units)
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
