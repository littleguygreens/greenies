// handlers_update.go — in-app update handlers.
//
// These two handlers power the "Check for Updates" button on the settings page.
//
//	GET /update/check  — asks GitHub for the latest release and compares it
//	                     against the version baked into this binary. Returns
//	                     a short JSON blob the browser JS reads to decide what
//	                     to show (up to date, or update available).
//
//	POST /update/apply — downloads the new binary and replaces the one on
//	                     disk. Returns plain text describing what happened.
//	                     The grower closes and reopens the app to run it.
package gui

import (
	"encoding/json"
	"net/http"

	"github.com/littleguygreens/greenies/internal/updater"
)

// repoOwner and repoName identify the GitHub repository that hosts releases.
const repoOwner = "littleguygreens"
const repoName = "greenies"

// releasesPageURL is the human-facing GitHub releases page. Shown as a
// fallback link on platforms (e.g. Android) where the binary can't be
// replaced in-place and the user must download and install manually.
const releasesPageURL = "https://github.com/" + repoOwner + "/" + repoName + "/releases/latest"

// updateCheckResult is what we send back to the browser after checking GitHub.
// The JS on the settings page reads these fields to decide what to display.
type updateCheckResult struct {
	// Current is the version string baked into the running binary, e.g. "v1.1.0".
	Current string `json:"current"`
	// Latest is the newest release tag on GitHub, e.g. "v1.2.0".
	Latest string `json:"latest"`
	// UpdateAvailable is true when Latest differs from Current.
	UpdateAvailable bool `json:"update_available"`
	// DownloadURL is the direct link to the new binary for this platform.
	// Empty when UpdateAvailable is false, or when no matching asset exists
	// for this platform (e.g. Android — the APK must be installed via the
	// system package installer, not replaced in-place).
	DownloadURL string `json:"download_url"`
	// ReleasesURL is always the human-facing GitHub releases page. Used as
	// a fallback link when DownloadURL is empty but an update is available.
	ReleasesURL string `json:"releases_url"`
	// Error holds a human-readable message if something went wrong.
	Error string `json:"error,omitempty"`
}

// handleUpdateCheck contacts the GitHub releases API and returns version
// information as JSON. The settings page JS reads this to show either
// "You're up to date" or an "Apply update" button.
func handleUpdateCheck(currentVersion string) http.HandlerFunc {
	// We wrap the handler in a closure so it has access to currentVersion
	// (the Version constant from main) without needing a global variable.
	return func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")

		rel, err := updater.CheckLatest(repoOwner, repoName)
		if err != nil {
			json.NewEncoder(w).Encode(updateCheckResult{
				Current:     currentVersion,
				ReleasesURL: releasesPageURL,
				Error:       "Could not reach GitHub: " + err.Error(),
			})
			return
		}

		json.NewEncoder(w).Encode(updateCheckResult{
			Current:         currentVersion,
			Latest:          rel.TagName,
			UpdateAvailable: rel.TagName != currentVersion,
			DownloadURL:     rel.DownloadURL,
			ReleasesURL:     releasesPageURL,
		})
	}
}

// handleUpdateApply downloads the new binary and replaces the running binary
// on disk. The grower must close and reopen the app to run the new version.
// This handler returns plain text so the JS can display it directly.
//
// SECURITY: this handler deliberately does NOT accept a download address
// from the browser. It asks GitHub for the latest release itself and uses
// the address GitHub returns. If we took the address as a request
// parameter, any website open in a browser on this machine could send
// requests to a local server like this one and point the updater at a
// fake "greenies" binary. By looking up the address server-side, the only
// thing a request can ever install is the genuine latest release from our
// own GitHub releases page.
func handleUpdateApply(currentVersion string) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/plain")

		// Ask GitHub for the latest release — the same lookup the "Check for
		// Updates" button does. We repeat it here rather than trusting the
		// browser to tell us the answer.
		rel, err := updater.CheckLatest(repoOwner, repoName)
		if err != nil {
			http.Error(w, "Could not reach GitHub: "+err.Error(), http.StatusBadGateway)
			return
		}

		if rel.TagName == currentVersion {
			http.Error(w, "Already running the latest version ("+currentVersion+").", http.StatusConflict)
			return
		}

		if rel.DownloadURL == "" {
			http.Error(w, "No download available for this platform — get it from "+releasesPageURL, http.StatusNotFound)
			return
		}

		if err := updater.ApplyUpdate(rel.DownloadURL); err != nil {
			http.Error(w, "Update failed: "+err.Error(), http.StatusInternalServerError)
			return
		}

		w.Write([]byte("Update applied. Close and reopen Greenies to run the new version."))
	}
}
