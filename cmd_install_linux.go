// Build tag: only compile this file on Linux. Go reads this line and skips the
// entire file when building for Windows, macOS, or any other operating system.
//go:build linux

package main

// cmd_install_linux.go handles the "greenies install-desktop" command on Linux.
//
// It creates a .desktop file so the grower can launch Greenies by
// double-clicking an icon on their desktop or finding it in the app menu —
// no terminal needed. The .desktop file is a standard Linux shortcut format
// that every desktop environment (Cinnamon, GNOME, KDE, etc.) understands.
//
// The shortcut is set to Terminal=false, which means Linux will run the
// program in the background without opening a terminal window. The GUI
// opens in the browser and the grower never sees a command line.
//
// Each operating system has its own version of this file:
//   cmd_install_linux.go   — .desktop file (this file)
//   cmd_install_darwin.go  — .app bundle for macOS
//   cmd_install_windows.go — not needed (just double-click greenies.exe)

import (
	_ "embed"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
)

// appIcon is the Greenies sprout icon (white on dark green, 192×192 PNG)
// embedded directly into the binary at compile time. This means the icon
// travels with the program — no external image file needed.
//
//go:embed desktop/icon.png
var appIcon []byte

// findBinaryPath returns the absolute path of the compiled Greenies binary.
//
// The tricky case: when a developer runs "go run . install-desktop" instead
// of the compiled binary, os.Executable() returns a path inside Go's
// temporary build cache (~/.cache/go-build/...). That path is wiped between
// builds, so a shortcut pointing there breaks immediately.
//
// To protect against this, we detect the build-cache situation and look for
// a real compiled binary in the most likely locations (~/greenies/). If we
// can't find one, we print a clear error telling the grower to build first.
func findBinaryPath() (string, error) {
	exe, err := os.Executable()
	if err != nil {
		return "", fmt.Errorf("could not find binary path: %w", err)
	}

	// EvalSymlinks resolves any filesystem shortcuts so we get the true path.
	exe, err = filepath.EvalSymlinks(exe)
	if err != nil {
		return "", fmt.Errorf("could not resolve binary path: %w", err)
	}

	// Detect if we're running from Go's temporary build cache. This happens
	// when the grower uses "go run ." instead of a compiled binary.
	// The build cache path always contains ".cache/go-build".
	if strings.Contains(exe, ".cache/go-build") {
		home, _ := os.UserHomeDir()

		// Look for a compiled binary in the standard locations.
		candidates := []string{
			filepath.Join(home, "greenies", "greenies-linux-arm64"),
			filepath.Join(home, "greenies", "greenies"),
		}
		for _, c := range candidates {
			if _, err := os.Stat(c); err == nil {
				fmt.Printf("Note: detected go run — using compiled binary at %s\n", c)
				return c, nil
			}
		}

		// No compiled binary found — tell the grower how to fix it.
		return "", fmt.Errorf(
			"install-desktop was run via 'go run', which uses a temporary\n"+
				"build cache path that is wiped between builds.\n\n"+
				"Please build the binary first, then install:\n"+
				"  go build -o greenies-linux-arm64 .\n"+
				"  ./greenies-linux-arm64 install-desktop",
		)
	}

	return exe, nil
}

// runInstallDesktop creates a stable launcher symlink and .desktop shortcut
// files so the grower can launch Greenies by double-clicking an icon.
//
// The shortcut does NOT point directly at the binary. Instead it points to a
// symlink we create at ~/.local/bin/greenies. This means rebuilding the binary
// (which replaces the file at the same path) never breaks the shortcut —
// the symlink still points to the right place without any re-installation.
//
// Files created:
//   ~/.local/bin/greenies              — symlink → the compiled binary
//   ~/Desktop/greenies.desktop         — double-clickable icon
//   ~/.local/share/applications/...    — app menu entry
func runInstallDesktop() {
	exePath, err := findBinaryPath()
	if err != nil {
		fmt.Printf("Error: %v\n", err)
		os.Exit(1)
	}

	home, err := os.UserHomeDir()
	if err != nil {
		fmt.Printf("Error: could not find home directory: %v\n", err)
		os.Exit(1)
	}

	// ── Install the app icon ────────────────────────────────────────────
	//
	// Copy the embedded PNG icon to ~/.local/share/icons/ — the standard
	// location for user-installed icons on Linux. Every desktop environment
	// (Cinnamon, GNOME, KDE, etc.) looks here for icons referenced by
	// .desktop files.
	iconDir := filepath.Join(home, ".local", "share", "icons")
	if err := os.MkdirAll(iconDir, 0755); err != nil {
		fmt.Printf("Warning: could not create icon directory %s: %v\n", iconDir, err)
	}
	iconPath := filepath.Join(iconDir, "greenies.png")
	if err := os.WriteFile(iconPath, appIcon, 0644); err != nil {
		fmt.Printf("Warning: could not write icon %s: %v\n", iconPath, err)
	} else {
		fmt.Printf("Installed icon: %s\n", iconPath)
	}

	// ── Install the launcher symlink ────────────────────────────────────
	//
	// Create ~/.local/bin/greenies as a symlink pointing to the compiled
	// binary. The .desktop file's Exec line always uses THIS symlink path,
	// never the binary path directly.
	//
	// Why a symlink? Rebuilding the binary with "go build -o greenies-linux-arm64 ."
	// replaces the binary file at its path but leaves the symlink alone —
	// the symlink still points to the same location, so the shortcut keeps
	// working without any re-installation.
	binDir := filepath.Join(home, ".local", "bin")
	if err := os.MkdirAll(binDir, 0755); err != nil {
		fmt.Printf("Warning: could not create %s: %v\n", binDir, err)
	}
	linkPath := filepath.Join(binDir, "greenies")

	// Remove any previous symlink so we can create a fresh one.
	// os.Remove on a missing file is harmless.
	_ = os.Remove(linkPath)
	if err := os.Symlink(exePath, linkPath); err != nil {
		// Symlink creation failed — fall back to pointing directly at the binary.
		// This still works, but rebuilds may break the shortcut again.
		fmt.Printf("Warning: could not create launcher symlink at %s: %v\n", linkPath, err)
		fmt.Printf("         Falling back to direct binary path — rebuild may break the shortcut.\n")
		linkPath = exePath
	} else {
		fmt.Printf("Installed launcher: %s → %s\n", linkPath, exePath)
	}

	// Build the .desktop file content. This is a standard format defined by
	// freedesktop.org — every Linux desktop environment knows how to read it.
	//
	// Key fields:
	//   Name          — what appears under the icon
	//   Exec          — the command to run when double-clicked (uses the symlink)
	//   Icon          — path to the PNG icon shown on the desktop and app menu
	//   Terminal      — false means "don't open a terminal window"
	//   Type          — "Application" is the standard type for programs
	//   Comment       — tooltip text shown when hovering over the icon
	//   Categories    — where it appears in the app menu (Office = productivity)
	//   StartupNotify — false because the GUI opens in the browser, not as a
	//                   native window, so the desktop shouldn't show a spinner
	desktopEntry := fmt.Sprintf(`[Desktop Entry]
Name=Greenies
Comment=Microgreens farm scheduler
Exec=%s gui
Icon=%s
Terminal=false
Type=Application
Categories=Office;
StartupNotify=false
`, linkPath, iconPath)

	// ── Place 1: the grower's Desktop folder ────────────────────────────
	desktopDir := filepath.Join(home, "Desktop")
	desktopPath := filepath.Join(desktopDir, "greenies.desktop")

	// Only create the Desktop shortcut if the Desktop folder exists.
	// Some minimal Linux setups don't have one.
	if _, err := os.Stat(desktopDir); err == nil {
		if err := os.WriteFile(desktopPath, []byte(desktopEntry), 0755); err != nil {
			fmt.Printf("Warning: could not write %s: %v\n", desktopPath, err)
		} else {
			fmt.Printf("Created desktop shortcut: %s\n", desktopPath)

			// On some Linux desktops (including Cinnamon on Linux Mint),
			// new .desktop files on the Desktop are "untrusted" by default.
			// The gio command marks it as trusted so the grower can
			// double-click it without getting a security warning.
			cmd := exec.Command("gio", "set", desktopPath,
				"metadata::trusted", "true")
			_ = cmd.Run() // Best effort — not all systems have gio.
		}
	}

	// ── Place 2: the app menu ───────────────────────────────────────────
	// ~/.local/share/applications/ is where user-installed app shortcuts live.
	// Programs placed here show up in the system's app menu / launcher.
	appDir := filepath.Join(home, ".local", "share", "applications")
	if err := os.MkdirAll(appDir, 0755); err != nil {
		fmt.Printf("Warning: could not create %s: %v\n", appDir, err)
	} else {
		appPath := filepath.Join(appDir, "greenies.desktop")
		if err := os.WriteFile(appPath, []byte(desktopEntry), 0644); err != nil {
			fmt.Printf("Warning: could not write %s: %v\n", appPath, err)
		} else {
			fmt.Printf("Created app menu entry: %s\n", appPath)
		}
	}

	fmt.Println("\nDone! You should now see Greenies in your app menu.")
	fmt.Println("Rebuilding the binary will not break the shortcut.")
	fmt.Println("If there's an icon on your Desktop, you can double-click it to launch the GUI.")
}
