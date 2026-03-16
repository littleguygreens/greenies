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
	"embed"
	"fmt"
	"html/template"
	"io/fs"
	"net/http"
	"os/exec"
	"runtime"
	"strings"
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
	err = templates.ExecuteTemplate(w, "layout.html", map[string]any{
		"Content": template.HTML(buf.String()),
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

// StartServer launches the web server on the given port. This is the main
// entry point called by cmd_gui.go when the user runs "greenies gui".
//
// It loads templates, registers URL routes (which URL goes to which handler
// function), and starts listening for browser requests. The server runs
// until the user presses Ctrl+C in the terminal.
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

	// ── Page routes ──────────────────────────────────────────────────────
	// Each line maps a URL to a handler function defined in handlers.go.
	// GET = the browser is asking to see a page.
	// POST = the browser is sending data (form submission, button click).

	mux.HandleFunc("GET /{$}", handleDashboard)
	mux.HandleFunc("GET /snapshot", handleSnapshot)
	mux.HandleFunc("GET /list", handleList)
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

	// Step 3: Start listening for requests.
	addr := fmt.Sprintf("127.0.0.1:%d", port)
	fmt.Printf("Greenies GUI running at http://%s\n", addr)
	fmt.Println("Press Ctrl+C to stop the server.")

	// Try to open the browser automatically. If it fails, no big deal —
	// the user can copy the URL from the terminal.
	go openBrowser(fmt.Sprintf("http://%s", addr))

	return http.ListenAndServe(addr, mux)
}

// openBrowser tries to open the given URL in the user's default browser.
// On Linux this uses xdg-open. On macOS it would use "open", on Windows
// "start". We only need Linux for now since that's what the farm runs.
func openBrowser(url string) {
	var cmd *exec.Cmd
	switch runtime.GOOS {
	case "linux":
		cmd = exec.Command("xdg-open", url)
	case "darwin":
		cmd = exec.Command("open", url)
	case "windows":
		cmd = exec.Command("rundll32", "url.dll,FileProtocolHandler", url)
	default:
		return // unsupported OS — user will open manually
	}
	// Run in the background and ignore errors — if the browser doesn't
	// open, the URL is printed in the terminal for manual copy.
	_ = cmd.Start()
}
