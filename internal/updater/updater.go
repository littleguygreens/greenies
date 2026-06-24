// Package updater handles checking for new Greenies releases on GitHub and
// downloading them to replace the running binary.
//
// How it works:
//  1. CheckLatest asks the GitHub API "what is the newest release?"
//  2. The caller compares that tag against the Version constant baked into
//     the binary. If the tag is newer, an update is available.
//  3. ApplyUpdate downloads the right binary asset for this platform,
//     writes it to a temporary file next to the current binary, then
//     renames it over the current binary in one atomic step.
//  4. The grower closes and reopens the app to run the new version.
package updater

import (
	"encoding/json" // for reading the GitHub API's JSON response
	"fmt"
	"io"      // for copying the downloaded bytes to a file
	"net/http" // for making HTTPS requests to the GitHub API and CDN
	"os"      // for file operations (create temp file, rename, chmod)
	"path/filepath" // for building the temp file path next to the binary
	"runtime" // for detecting the operating system and CPU architecture
)

// Release holds the information we care about from a GitHub release.
type Release struct {
	// TagName is the version string — e.g. "v1.2.0".
	TagName string `json:"tag_name"`

	// DownloadURL is the direct download link for the binary asset that
	// matches this machine's platform. Empty if no matching asset was found.
	DownloadURL string
}

// asset mirrors the shape of one entry in the GitHub API's "assets" array.
// We only need two fields from the full JSON object.
type asset struct {
	Name               string `json:"name"`
	BrowserDownloadURL string `json:"browser_download_url"`
}

// githubRelease is the full shape of the GitHub API response.
// We pull out the tag name and the asset list.
type githubRelease struct {
	TagName string  `json:"tag_name"`
	Assets  []asset `json:"assets"`
}

// assetName returns the expected file name for the Greenies binary on this
// machine. For example, on a 64-bit Linux ARM machine it returns
// "greenies-linux-arm64". On Windows it adds ".exe" to match the release
// asset naming convention. This must match the asset names in the GitHub
// release — if the naming convention ever changes, update it here.
func assetName() string {
	// runtime.GOOS  is the operating system:   "linux", "darwin", "windows"
	// runtime.GOARCH is the CPU architecture: "amd64", "arm64", "arm"
	name := fmt.Sprintf("greenies-%s-%s", runtime.GOOS, runtime.GOARCH)
	if runtime.GOOS == "windows" {
		name += ".exe"
	}
	return name
}

// CheckLatest asks the GitHub releases API for the newest release and returns
// a Release containing its tag and the download URL for this platform's binary.
//
// Returns an error if the network request fails or the response can't be parsed.
// Returns a Release with an empty DownloadURL if no asset matches this platform.
func CheckLatest(repoOwner, repoName string) (Release, error) {
	url := fmt.Sprintf("https://api.github.com/repos/%s/%s/releases/latest", repoOwner, repoName)

	// Build the request with a User-Agent header. The GitHub API requires one
	// — requests without it are rejected.
	req, err := http.NewRequest("GET", url, nil)
	if err != nil {
		return Release{}, fmt.Errorf("could not build request: %w", err)
	}
	req.Header.Set("User-Agent", "greenies-updater")
	req.Header.Set("Accept", "application/vnd.github+json")

	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return Release{}, fmt.Errorf("could not reach GitHub: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		return Release{}, fmt.Errorf("GitHub returned status %d", resp.StatusCode)
	}

	var gr githubRelease
	if err := json.NewDecoder(resp.Body).Decode(&gr); err != nil {
		return Release{}, fmt.Errorf("could not read GitHub response: %w", err)
	}

	// Find the asset whose name matches this platform.
	target := assetName()
	rel := Release{TagName: gr.TagName}
	for _, a := range gr.Assets {
		if a.Name == target {
			rel.DownloadURL = a.BrowserDownloadURL
			break
		}
	}

	return rel, nil
}

// ApplyUpdate downloads the binary at downloadURL, writes it next to the
// currently-running binary, marks it executable, and renames it over the
// current binary in one atomic step.
//
// On Linux you can rename over a running binary — the OS keeps the old file
// alive for the current process while the new file is available for the next
// launch. The grower just needs to close and reopen the app.
//
// Returns an error if any step fails. On error the original binary is always
// left in place — we never delete it before the new one is ready.
func ApplyUpdate(downloadURL string) error {
	// Find the full path of the currently running binary.
	// os.Executable() returns something like "/usr/local/bin/greenies".
	exePath, err := os.Executable()
	if err != nil {
		return fmt.Errorf("could not find current binary path: %w", err)
	}

	// Download the new binary.
	resp, err := http.Get(downloadURL) //nolint:noctx
	if err != nil {
		return fmt.Errorf("download failed: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("download returned status %d", resp.StatusCode)
	}

	// Write to a temporary file in the same directory as the current binary.
	// Using the same directory is important — os.Rename only works atomically
	// when source and destination are on the same filesystem. Writing to /tmp
	// could fail on Rename if /tmp is a different mount than the binary.
	dir := filepath.Dir(exePath)
	tmp, err := os.CreateTemp(dir, "greenies-update-*")
	if err != nil {
		return fmt.Errorf("could not create temporary file: %w", err)
	}
	tmpPath := tmp.Name()

	// Make sure the temp file is cleaned up if something goes wrong before
	// the rename. After a successful rename the temp path no longer exists,
	// so Remove on a missing file is harmless.
	defer os.Remove(tmpPath)

	// Copy the downloaded bytes into the temp file.
	if _, err := io.Copy(tmp, resp.Body); err != nil {
		tmp.Close()
		return fmt.Errorf("could not write update to disk: %w", err)
	}
	tmp.Close()

	// Mark the new binary executable (rwxr-xr-x).
	if err := os.Chmod(tmpPath, 0755); err != nil {
		return fmt.Errorf("could not make update executable: %w", err)
	}

	// Rename the temp file over the current binary. On Linux this is atomic —
	// either the old file is there or the new one is, never a half-written state.
	if err := os.Rename(tmpPath, exePath); err != nil {
		return fmt.Errorf("could not replace binary: %w", err)
	}

	return nil
}
