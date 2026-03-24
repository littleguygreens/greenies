// Build tag: only compile this file on macOS. Go reads this line and skips
// the entire file when building for Linux, Windows, or any other operating
// system. ("darwin" is the internal name macOS uses — named after Charles
// Darwin for historical Apple reasons.)
//go:build darwin

package main

// cmd_install_darwin.go handles the "greenies install-desktop" command on macOS.
//
// It creates a .app bundle so the grower can launch Greenies by
// double-clicking an icon in the Applications folder or Launchpad — no
// terminal needed. A .app bundle is just a folder with a specific layout
// that macOS recognises as an application.
//
// The bundle structure looks like this:
//
//	Greenies.app/
//	  Contents/
//	    Info.plist        <- config file macOS reads (app name, icon, version)
//	    MacOS/
//	      greenies        <- the actual program binary
//	    Resources/
//	      greenies.png    <- the app icon
//
// Each operating system has its own version of this file:
//
//	cmd_install_linux.go   — .desktop file for Linux
//	cmd_install_darwin.go  — .app bundle for macOS (this file)
//	cmd_install_windows.go — not needed (just double-click greenies.exe)

import (
	_ "embed"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
)

// appIcon is the Greenies sprout icon (white on dark green, 192×192 PNG)
// embedded directly into the binary at compile time. This means the icon
// travels with the program — no external image file needed.
//
//go:embed desktop/icon.png
var appIcon []byte

// infoPlist is the configuration file that macOS reads to learn about the
// app. It tells macOS:
//   - CFBundleName: the name shown under the icon ("Greenies")
//   - CFBundleExecutable: which file inside MacOS/ to run ("greenies")
//   - CFBundleIconFile: which icon to display ("greenies.png")
//   - LSUIElement: "1" means "don't show in the Dock" — the GUI opens in
//     the browser, so there's nothing useful to show in the Dock
//   - CFBundleIdentifier: a unique reverse-domain ID that macOS uses
//     internally to tell apps apart
const infoPlist = `<?xml version="1.0" encoding="UTF-8"?>
<!DOCTYPE plist PUBLIC "-//Apple//DTD PLIST 1.0//EN"
  "http://www.apple.com/DTDs/PropertyList-1.0.dtd">
<plist version="1.0">
<dict>
    <key>CFBundleName</key>
    <string>Greenies</string>
    <key>CFBundleExecutable</key>
    <string>greenies</string>
    <key>CFBundleIconFile</key>
    <string>greenies.png</string>
    <key>CFBundleIdentifier</key>
    <string>com.littleguygreens.greenies</string>
    <key>CFBundleVersion</key>
    <string>1.0.0</string>
    <key>LSUIElement</key>
    <string>1</string>
</dict>
</plist>
`

// runInstallDesktop creates a Greenies.app bundle in ~/Applications/.
//
// ~/Applications is the per-user Applications folder. It works exactly like
// /Applications but doesn't need administrator (sudo) permission. macOS
// treats it the same — apps here show up in Launchpad and Spotlight search.
func runInstallDesktop() {
	// Find the full path to the greenies binary that is currently running.
	exePath, err := os.Executable()
	if err != nil {
		fmt.Printf("Error: could not find the greenies binary path: %v\n", err)
		os.Exit(1)
	}

	// Resolve any symbolic links (shortcuts within the filesystem) so the
	// .app bundle contains the actual binary, not a link that might break.
	exePath, err = filepath.EvalSymlinks(exePath)
	if err != nil {
		fmt.Printf("Error: could not resolve binary path: %v\n", err)
		os.Exit(1)
	}

	home, err := os.UserHomeDir()
	if err != nil {
		fmt.Printf("Error: could not find home directory: %v\n", err)
		os.Exit(1)
	}

	// ── Create the .app bundle folder structure ─────────────────────────
	//
	// ~/Applications/Greenies.app/Contents/MacOS/   <- binary goes here
	// ~/Applications/Greenies.app/Contents/Resources/ <- icon goes here
	appDir := filepath.Join(home, "Applications", "Greenies.app", "Contents")
	macosDir := filepath.Join(appDir, "MacOS")
	resDir := filepath.Join(appDir, "Resources")

	for _, dir := range []string{macosDir, resDir} {
		if err := os.MkdirAll(dir, 0755); err != nil {
			fmt.Printf("Error: could not create %s: %v\n", dir, err)
			os.Exit(1)
		}
	}

	// ── Copy the binary into the bundle ─────────────────────────────────
	//
	// We read the currently-running binary and write a copy into the .app
	// bundle. This way the bundle is self-contained — you could delete the
	// original binary and the app would still work.
	binData, err := os.ReadFile(exePath)
	if err != nil {
		fmt.Printf("Error: could not read binary %s: %v\n", exePath, err)
		os.Exit(1)
	}

	bundleBin := filepath.Join(macosDir, "greenies")
	// 0755 means the file is executable (the owner can read, write, and run
	// it; everyone else can read and run it). Without this, macOS would
	// refuse to launch the binary.
	if err := os.WriteFile(bundleBin, binData, 0755); err != nil {
		fmt.Printf("Error: could not write binary to bundle: %v\n", err)
		os.Exit(1)
	}
	fmt.Printf("Installed binary: %s\n", bundleBin)

	// ── Write the icon ──────────────────────────────────────────────────
	iconPath := filepath.Join(resDir, "greenies.png")
	if err := os.WriteFile(iconPath, appIcon, 0644); err != nil {
		fmt.Printf("Warning: could not write icon: %v\n", err)
	} else {
		fmt.Printf("Installed icon: %s\n", iconPath)
	}

	// ── Write Info.plist ────────────────────────────────────────────────
	plistPath := filepath.Join(appDir, "Info.plist")
	if err := os.WriteFile(plistPath, []byte(infoPlist), 0644); err != nil {
		fmt.Printf("Error: could not write Info.plist: %v\n", err)
		os.Exit(1)
	}
	fmt.Printf("Created Info.plist: %s\n", plistPath)

	// ── Tell macOS to re-scan the app ───────────────────────────────────
	//
	// macOS caches information about installed apps. After creating a new
	// .app bundle, we poke the Launch Services database so macOS picks it
	// up immediately — otherwise it might not appear in Launchpad or
	// Spotlight until the next restart.
	bundlePath := filepath.Join(home, "Applications", "Greenies.app")
	cmd := exec.Command("/System/Library/Frameworks/CoreServices.framework/Frameworks/LaunchServices.framework/Support/lsregister",
		"-f", bundlePath)
	_ = cmd.Run() // Best effort — the app still works without this.

	fmt.Println("\nDone! Greenies.app has been installed to ~/Applications/.")
	fmt.Println("You should find it in Launchpad and Spotlight search.")
	fmt.Println("You can also drag it to your Dock for quick access.")
}
