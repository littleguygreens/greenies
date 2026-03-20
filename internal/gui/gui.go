// Package gui provides a browser-based interface for the greenies scheduler.
//
// When the user runs "greenies gui", this package starts a tiny web server on
// their own computer (localhost:8080). They open a browser and see a dashboard
// with pages for every CLI command — buttons and forms instead of typing.
//
// Everything runs locally. No internet connection is needed (except for Google
// Calendar sync, which already requires it). The server is only accessible from
// the user's own machine — nobody else on the network can reach it.
//
// All the HTML templates, the CSS stylesheet, and the htmx JavaScript library
// are embedded directly into the Go binary using Go's "embed" package. This
// means the binary stays a single portable file — no extra folders to copy.
package gui

import (
	"bytes"
	"context" // for graceful server shutdown
	"embed"
	"fmt"
	"html/template"
	"io/fs"
	"net/http"
	"os/exec"
	"runtime"
	"strings"
	"sync"  // for sync.Mutex — a lock that prevents two things from reading/writing at the same time
	"time"

	"github.com/littleguygreens/greenies/internal/config"
)

// ─────────────────────────────────────────────────────────────────────────────
// Embedded files
// ─────────────────────────────────────────────────────────────────────────────
//
// Go's "embed" feature lets us bake files into the compiled binary at build
// time. The //go:embed directive (a special comment that Go's compiler reads)
// tells it which files to include.
//
// After building, the binary contains a copy of every file under static/ and
// templates/. At runtime, we read them from memory — no disk access needed.

//go:embed static/*
var staticFiles embed.FS

//go:embed templates/*
var templateFiles embed.FS

// ─────────────────────────────────────────────────────────────────────────────
// Template engine
// ─────────────────────────────────────────────────────────────────────────────
//
// Go's html/template package works like a mail merge in Google Docs:
//   - You write an HTML file with placeholders like {{.Title}}
//   - You pass Go data to the template
//   - The template engine fills in the placeholders and produces the final HTML
//
// We load all templates once at startup and keep them in memory. Each page
// request then just fills in the data and sends the result to the browser.

// templates holds all parsed HTML templates, ready to render.
var templates *template.Template

// funcMap defines custom helper functions available inside templates.
// These let templates do simple formatting that plain Go templates can't
// do on their own.
var funcMap = template.FuncMap{
	// "join" turns a list of strings into one string with a separator.
	// Example in a template: {{join .Tasks ", "}}
	"join": strings.Join,

	// Math helpers for templates. Go templates can't do arithmetic, but we
	// need it to calculate progress bar widths (e.g. "48 out of 64 = 75%").
	//
	// "itof" converts an integer to a floating-point number so we can do
	// division without losing the decimal part (12 / 64 = 0.1875, not 0).
	"itof": func(i int) float64 { return float64(i) },
	// "mulf" multiplies two floating-point numbers (e.g. 0.75 * 100 = 75).
	"mulf": func(a, b float64) float64 { return a * b },
	// "divf" divides two floating-point numbers (e.g. 48.0 / 64.0 = 0.75).
	// Returns 0 if the divisor is zero to avoid a crash.
	"divf": func(a, b float64) float64 {
		if b == 0 {
			return 0
		}
		return a / b
	},
}

// loadTemplates parses every HTML file in the templates/ directory and stores
// them in the package-level `templates` variable. Called once at server start.
func loadTemplates() error {
	var err error
	templates, err = template.New("").Funcs(funcMap).ParseFS(templateFiles, "templates/*.html")
	return err
}

// renderPage sends a complete HTML page to the browser by combining the
// layout template (which has the nav bar) with a content template (which
// has the page-specific HTML).
//
// How it works (two-step render):
//   1. Render the page-specific template (e.g. "dashboard.html") into a
//      string of HTML — this is the "content" of the page.
//   2. Pass that HTML string into layout.html, which wraps it with the
//      nav bar and page structure.
//
// We do it in two steps because Go's {{template}} directive requires a
// fixed name — you can't pass a variable. So we render the inner template
// first, then inject the result into the layout.
//
// Parameters:
//   - w: where to send the HTML (the browser's connection)
//   - name: which content template to use (e.g. "dashboard.html")
//   - data: the Go values that fill in the template's placeholders
func renderPage(w http.ResponseWriter, name string, data any) {
	// Step 1: Render the page content into a buffer (a temporary container
	// that collects the HTML as a string).
	var buf bytes.Buffer
	err := templates.ExecuteTemplate(&buf, name, data)
	if err != nil {
		http.Error(w, fmt.Sprintf("Template error: %v", err), http.StatusInternalServerError)
		return
	}

	// Step 2: Pass the rendered content into the layout template.
	// template.HTML tells Go "this is safe HTML, don't escape it" — without
	// this, the HTML tags would show as literal text on the page.
	//
	// We also load the user's config to pass cosmetic preferences (like
	// the lowercase toggle) to the layout so they apply on every page.
	cfg, _ := config.Load()

	err = templates.ExecuteTemplate(w, "layout.html", map[string]any{
		"Content":   template.HTML(buf.String()),
		"Lowercase": cfg.Lowercase,
		"Theme":     cfg.Theme,
	})
	if err != nil {
		http.Error(w, fmt.Sprintf("Layout error: %v", err), http.StatusInternalServerError)
	}
}

// renderFragment sends just a piece of HTML (not a full page) to the browser.
// Used by htmx — when the user clicks "Preview", htmx sends a request and
// swaps just the preview section of the page, without reloading everything.
func renderFragment(w http.ResponseWriter, name string, data any) {
	err := templates.ExecuteTemplate(w, name, data)
	if err != nil {
		http.Error(w, fmt.Sprintf("Template error: %v", err), http.StatusInternalServerError)
	}
}

// ─────────────────────────────────────────────────────────────────────────────
// Server
// ─────────────────────────────────────────────────────────────────────────────

// ─────────────────────────────────────────────────────────────────────────────
// Heartbeat — automatic shutdown when the browser window is closed
// ─────────────────────────────────────────────────────────────────────────────
//
// The browser page sends a tiny "I'm still here" ping to /heartbeat every 2
// seconds (see the <script> in layout.html). A background goroutine on the
// server watches for these pings. If none arrive for 6 seconds (3 missed
// beats), the server assumes the browser window was closed and shuts itself
// down cleanly — no need to press Ctrl+C in the terminal.
//
// Think of it like a dead man's switch: as long as someone is watching, the
// server stays alive. The moment nobody is watching, it stops on its own.

// heartbeatTimeout is how long the server waits without a ping before it
// decides the browser is gone and shuts down. 10 seconds is generous enough
// to survive page transitions and brief network hiccups without false
// shutdowns.
const heartbeatTimeout = 10 * time.Second

// lastHeartbeat records the time of the most recent ping from the browser.
// Protected by heartbeatMu because the HTTP handler (which receives pings)
// and the watcher goroutine (which checks the timestamp) run at the same
// time — without the lock, they could read/write simultaneously and cause
// unpredictable behaviour.
var lastHeartbeat time.Time
var heartbeatMu sync.Mutex

// browserConnected tracks whether the browser has ever sent a heartbeat
// ping. The watcher goroutine ignores the timeout until this is true —
// so on a slow machine, the server will wait patiently for the browser
// to finish loading instead of shutting down after 6 seconds.
var browserConnected bool

// recordHeartbeat updates the last-seen timestamp. Called by the /heartbeat
// HTTP handler every time the browser pings.
func recordHeartbeat() {
	heartbeatMu.Lock()
	lastHeartbeat = time.Now()
	browserConnected = true
	heartbeatMu.Unlock()
}

// heartbeatExpired checks whether the browser has gone silent for too long.
// Returns false if the browser hasn't connected yet — the server will wait
// indefinitely for the first ping (the grower can always press Ctrl+C).
// Once the browser has connected, returns true if no ping has arrived
// within the timeout window.
func heartbeatExpired() bool {
	heartbeatMu.Lock()
	connected := browserConnected
	elapsed := time.Since(lastHeartbeat)
	heartbeatMu.Unlock()
	// Don't shut down if the browser hasn't loaded yet — it might just
	// be a slow machine. Only start the countdown after the first ping.
	if !connected {
		return false
	}
	return elapsed > heartbeatTimeout
}

// watchHeartbeat runs in the background and checks every 2 seconds whether
// the browser is still pinging. If the heartbeat has expired, it tells the
// server to shut down gracefully — meaning it finishes any in-progress
// request (like a sync) before stopping, rather than cutting it off mid-way.
func watchHeartbeat(server *http.Server) {
	for {
		time.Sleep(2 * time.Second)
		if heartbeatExpired() {
			fmt.Println("\nBrowser window closed — shutting down.")
			// context.Background() means "take as long as you need" for the
			// graceful shutdown. In practice it's nearly instant because
			// there are no active requests (the browser is gone).
			_ = server.Shutdown(context.Background())
			return
		}
	}
}

// StartServer launches the web server on the given port. This is the main
// entry point called by cmd_gui.go when the user runs "greenies gui".
//
// It loads templates, registers URL routes (which URL goes to which handler
// function), and starts listening for browser requests. The server runs
// until the browser window is closed or the user presses Ctrl+C in the
// terminal — whichever comes first.
func StartServer(port int) error {
	// Step 1: Load all HTML templates into memory.
	if err := loadTemplates(); err != nil {
		return fmt.Errorf("failed to load templates: %w", err)
	}

	// Step 2: Set up the router — a table that maps URLs to handler functions.
	// Go's http.ServeMux is the standard library's built-in router.
	mux := http.NewServeMux()

	// Serve static files (CSS, JavaScript) from the embedded filesystem.
	// The browser requests these when loading a page — e.g. /static/style.css
	// or /static/htmx.min.js.
	staticSub, err := fs.Sub(staticFiles, "static")
	if err != nil {
		return fmt.Errorf("failed to set up static files: %w", err)
	}
	mux.Handle("GET /static/", http.StripPrefix("/static/", http.FileServer(http.FS(staticSub))))

	// ── Heartbeat endpoint ───────────────────────────────────────────────
	// The browser pings this every 2 seconds to say "I'm still open."
	// The handler just records the timestamp — no response body needed.
	mux.HandleFunc("GET /heartbeat", func(w http.ResponseWriter, r *http.Request) {
		recordHeartbeat()
		w.WriteHeader(http.StatusOK)
	})

	// ── Page routes ──────────────────────────────────────────────────────
	// Each line maps a URL to a handler function defined in handlers.go.
	// GET = the browser is asking to see a page.
	// POST = the browser is sending data (form submission, button click).

	mux.HandleFunc("GET /{$}", handleDashboard)
	mux.HandleFunc("GET /snapshot", handleSnapshot)
	mux.HandleFunc("GET /list", handleCalendar)
	mux.HandleFunc("GET /crops", handleCrops)
	mux.HandleFunc("GET /harvestlog", handleHarvestLog)
	mux.HandleFunc("GET /delete", handleDeletePage)
	mux.HandleFunc("GET /delete/confirm", handleDeleteConfirm)
	mux.HandleFunc("POST /delete", handleDeleteAction)
	mux.HandleFunc("GET /clear", handleClearPage)
	mux.HandleFunc("POST /clear", handleClearAction)
	mux.HandleFunc("GET /plan", handlePlanPage)
	mux.HandleFunc("POST /plan/preview", handlePlanPreview)
	mux.HandleFunc("POST /plan/confirm", handlePlanConfirm)
	mux.HandleFunc("GET /harvest", handleHarvestPage)
	mux.HandleFunc("POST /harvest", handleHarvestAction)
	mux.HandleFunc("GET /trial", handleTrialPage)
	mux.HandleFunc("POST /trial/new", handleTrialNew)
	mux.HandleFunc("GET /trial/manage", handleTrialManage)
	mux.HandleFunc("POST /trial/manage", handleTrialManageAction)
	mux.HandleFunc("POST /trial/outcome", handleTrialOutcome)
	mux.HandleFunc("POST /trial/promote", handleTrialPromote)
	mux.HandleFunc("POST /trial/discard", handleTrialDiscard)
	mux.HandleFunc("GET /trial/view", handleTrialView)
	mux.HandleFunc("GET /trial/compare", handleTrialCompare)
	mux.HandleFunc("GET /trial/compare-pick", handleTrialComparePicker)

	mux.HandleFunc("GET /adjust", handleAdjustPage)
	mux.HandleFunc("POST /adjust/preview", handleAdjustPreview)
	mux.HandleFunc("POST /adjust/confirm", handleAdjustConfirm)

	mux.HandleFunc("GET /settings", handleSettingsPage)
	mux.HandleFunc("POST /settings", handleSettingsUpdate)

	mux.HandleFunc("GET /sync", handleSyncPage)
	mux.HandleFunc("POST /sync", handleSyncAction)
	mux.HandleFunc("POST /google-signin", handleGoogleSignIn)
	mux.HandleFunc("POST /sheets-setup", handleSheetsSetup)

	// ── Open terminal ───────────────────────────────────────────────
	// When the grower clicks "Open Terminal" in the nav bar, the browser
	// sends a POST here. The server opens their system's terminal app
	// (set to the home directory) so they can type greenies commands.
	mux.HandleFunc("POST /terminal", func(w http.ResponseWriter, r *http.Request) {
		openTerminal()
		w.WriteHeader(http.StatusNoContent)
	})

	// Step 3: Create the server and start listening.
	// We use http.Server (instead of the simpler http.ListenAndServe) so
	// that we can call server.Shutdown() later — that's what lets us stop
	// the server cleanly when the browser window is closed.
	//
	// We wrap the mux in a heartbeat middleware so that ANY request from
	// the browser (page loads, form submissions, static file requests)
	// counts as proof the browser is still open — not just the dedicated
	// /heartbeat pings. This prevents false shutdowns during slow page
	// transitions where the old page has unloaded but the new page
	// hasn't started sending heartbeat pings yet.
	addr := fmt.Sprintf("127.0.0.1:%d", port)
	handler := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		recordHeartbeat()
		mux.ServeHTTP(w, r)
	})
	server := &http.Server{
		Addr:    addr,
		Handler: handler,
	}

	fmt.Printf("Greenies GUI running at http://%s\n", addr)
	fmt.Println("Close the browser window or press Ctrl+C to stop.")

	// Try to open the browser automatically. If it fails, no big deal —
	// the user can copy the URL from the terminal.
	go openBrowser(fmt.Sprintf("http://%s", addr))

	// Start the heartbeat watcher in the background. It will wait
	// patiently until the browser sends its first ping (no timeout
	// until then), then start monitoring. If the browser stops pinging,
	// it calls server.Shutdown().
	go watchHeartbeat(server)

	// ListenAndServe blocks (waits) until the server is shut down.
	// When server.Shutdown() is called (by the heartbeat watcher or
	// Ctrl+C), ListenAndServe returns http.ErrServerClosed — which is
	// not a real error, just Go's way of saying "the server stopped
	// because you asked it to."
	err = server.ListenAndServe()
	if err == http.ErrServerClosed {
		return nil // Clean shutdown — not an error.
	}
	return err
}

// openTerminal launches the system's terminal application so the grower can
// type greenies commands without leaving the GUI. On Linux, it uses
// x-terminal-emulator, which opens whatever terminal the user has configured
// (e.g. GNOME Terminal on Cinnamon, Konsole on KDE). On macOS it opens
// Terminal.app, and on Windows it opens PowerShell.
func openTerminal() {
	switch runtime.GOOS {
	case "linux":
		cmd := exec.Command("x-terminal-emulator")
		_ = cmd.Start()
	case "darwin":
		cmd := exec.Command("open", "-a", "Terminal")
		_ = cmd.Start()
	case "windows":
		cmd := exec.Command("cmd", "/c", "start", "powershell")
		_ = cmd.Start()
	}
}

// openBrowser tries to open the given URL in a borderless "app" window.
//
// On Linux, it first tries to find Chromium or Chrome and launch with the
// --app flag. This opens the page in its own window with no address bar,
// no tabs, and no bookmarks bar — it looks like a real desktop app rather
// than a website. The manifest.json file we serve tells the browser the
// app name and theme colour.
//
// If neither Chromium nor Chrome is installed, it falls back to xdg-open,
// which opens the URL in whatever the default browser is (with full browser
// chrome — address bar, tabs, etc.).
func openBrowser(url string) {
	switch runtime.GOOS {
	case "linux":
		// Try Chromium-based browsers first — they support the --app flag
		// which gives us a clean, borderless window.
		// exec.LookPath checks if a program is installed by searching the
		// system PATH (the list of directories where programs live).
		for _, browser := range []string{"chromium-browser", "chromium", "google-chrome", "google-chrome-stable"} {
			path, err := exec.LookPath(browser)
			if err == nil {
				// Found a Chromium-based browser — launch in app mode.
				// --app=URL opens the page in a standalone window with no
				// browser chrome. It looks like a native desktop application.
				cmd := exec.Command(path, "--app="+url)
				_ = cmd.Start()
				return
			}
		}
		// No Chromium found — fall back to opening in the default browser.
		// This will have the normal address bar and tabs, but still works.
		cmd := exec.Command("xdg-open", url)
		_ = cmd.Start()
	case "darwin":
		// macOS: try Chrome app mode, fall back to "open" (default browser).
		path, err := exec.LookPath("google-chrome")
		if err == nil {
			cmd := exec.Command(path, "--app="+url)
			_ = cmd.Start()
			return
		}
		cmd := exec.Command("open", url)
		_ = cmd.Start()
	case "windows":
		cmd := exec.Command("rundll32", "url.dll,FileProtocolHandler", url)
		_ = cmd.Start()
	}
	// On unsupported OS or if everything fails, the URL is printed in the
	// terminal for the user to copy manually.
}
