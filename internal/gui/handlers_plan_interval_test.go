package gui

// Automated tests for the "Repeat every ___ weeks" dropdown on the Plan page.
// Run with "go test ./..." from the project root.

import (
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/littleguygreens/greenies/internal/farm"
)

// TestParseRepeatInterval checks that the dropdown value is read correctly,
// and that anything unexpected falls back to weekly — so an old form that
// never sent the field keeps working exactly as it did before.
func TestParseRepeatInterval(t *testing.T) {
	cases := []struct {
		in   string
		want int
	}{
		{"1", 1},
		{"2", 2},
		{"3", 3},
		{"4", 4},
		{"6", 6},   // the longest allowed gap
		{"", 1},    // field missing (old form) → weekly
		{"0", 1},   // nonsense → weekly
		{"7", 1},   // beyond the allowed range → weekly
		{"-2", 1},  // negative → weekly
		{"abc", 1}, // not a number → weekly
	}
	for _, c := range cases {
		if got := parseRepeatInterval(c.in); got != c.want {
			t.Errorf("parseRepeatInterval(%q) = %d, want %d", c.in, got, c.want)
		}
	}
}

// TestPlanRotationEveryThreeWeeks plans one crop through the real Plan page
// handlers — preview, then confirm — with "every 3 weeks" selected, and
// checks that the saved harvests land three weeks apart.
//
// This is the rotation the option was built for: plan each crop like this
// with first harvests on consecutive Saturdays and they take turns.
//
// HOME is pointed at a throwaway folder, so the test never touches the
// grower's real ~/.greenies data. The sample crops.csv and farm.csv from
// the project root are copied in as the crop library and farm layout.
func TestPlanRotationEveryThreeWeeks(t *testing.T) {
	// The page templates are normally loaded when the server starts.
	if err := loadTemplates(); err != nil {
		t.Fatal(err)
	}

	home := t.TempDir()
	t.Setenv("HOME", home)
	dataDir := filepath.Join(home, ".greenies")
	if err := os.MkdirAll(dataDir, 0o755); err != nil {
		t.Fatal(err)
	}
	for _, name := range []string{"crops.csv", "farm.csv"} {
		data, err := os.ReadFile(filepath.Join("..", "..", name))
		if err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(dataDir, name), data, 0o644); err != nil {
			t.Fatal(err)
		}
	}

	// Sunnies, 2 trays, first harvest Saturday Nov 7 2026, plus 3 repeats
	// every 3 weeks — four harvests in total.
	form := url.Values{
		"crop":      {"sunnies"},
		"plan_mode": {"trays"},
		"trays":     {"2"},
		"direction": {"harvest"},
		"date":      {"2026-11-07"},
		"lit_env":   {"main_tent"},
		"repeats":   {"3"},
		"interval":  {"3"},
	}
	post := func(handler http.HandlerFunc) string {
		req := httptest.NewRequest("POST", "/", strings.NewReader(form.Encode()))
		req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
		rec := httptest.NewRecorder()
		handler(rec, req)
		return rec.Body.String()
	}

	// The Plan page's dropdown should offer every gap up to the limit.
	pageRec := httptest.NewRecorder()
	handlePlanPage(pageRec, httptest.NewRequest("GET", "/plan", nil))
	page := pageRec.Body.String()
	for _, want := range []string{
		`<option value="1" selected>Every week</option>`,
		`<option value="3">Every 3 weeks</option>`,
		`<option value="6">Every 6 weeks</option>`,
	} {
		if !strings.Contains(page, want) {
			t.Errorf("plan page is missing dropdown choice %q", want)
		}
	}

	// The preview should describe the rhythm and the season's last harvest,
	// and carry the interval through to the confirm step.
	preview := post(handlePlanPreview)
	if want := "4 cycles, every 3 weeks — last harvest Sat Jan 09 2027."; !strings.Contains(preview, want) {
		t.Errorf("preview is missing %q", want)
	}
	if want := `name="interval" value="3"`; !strings.Contains(preview, want) {
		t.Errorf("preview is missing the hidden interval field %q", want)
	}

	post(handlePlanConfirm)

	cycles, err := farm.LoadCycles()
	if err != nil {
		t.Fatal(err)
	}
	var got []string
	for _, c := range cycles {
		got = append(got, c.HarvestDate)
	}
	want := []string{"2026-11-07", "2026-11-28", "2026-12-19", "2027-01-09"}
	if strings.Join(got, ",") != strings.Join(want, ",") {
		t.Errorf("saved harvest dates = %v, want %v", got, want)
	}
}
