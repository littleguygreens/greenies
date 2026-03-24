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
)

// appIcon is the Greenies sprout icon (white on dark green, 192×192 PNG)
// embedded directly into the binary at compile time. This means the icon
// travels with the program — no external image file needed.
//
//go:embed desktop/icon.png
var appIcon []byte

// runInstallDesktop creates a .desktop shortcut file in two places:
//   1. The grower's Desktop folder — so they see an icon to double-click
//   2. ~/.local/share/applications/ — so it appears in the app menu
//      (the searchable list of programs in the system tray)
func runInstallDesktop() {
	// Find the full path to the greenies binary. os.Executable() returns
	// the path of the currently running program — we use this so the
	// shortcut always points to the right place, even if the grower
	// moves the binary later (well, not if they move it, but it captures
	// wherever it is right now).
	exePath, err := os.Executable()
	if err != nil {
		fmt.Printf("Error: could not find the greenies binary path: %v\n", err)
		os.Exit(1)
	}

	// Resolve any symbolic links (shortcuts within the filesystem) so the
	// .desktop file points to the actual binary, not a link that might break.
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

	// Build the .desktop file content. This is a standard format defined by
	// freedesktop.org — every Linux desktop environment knows how to read it.
	//
	// Key fields:
	//   Name        — what appears under the icon
	//   Exec        — the command to run when double-clicked
	//   Icon        — path to the PNG icon shown on the desktop and app menu
	//   Terminal     — false means "don't open a terminal window"
	//   Type        — "Application" is the standard type for programs
	//   Comment     — tooltip text shown when hovering over the icon
	//   Categories  — where it appears in the app menu (Office = productivity)
	//   StartupNotify — false because the GUI opens in the browser, not as a
	//                    native window, so the desktop shouldn't show a spinner
	desktopEntry := fmt.Sprintf(`[Desktop Entry]
Name=Greenies
Comment=Microgreens farm scheduler
Exec=%s
Icon=%s
Terminal=false
Type=Application
Categories=Office;
StartupNotify=false
`, exePath, iconPath)

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
	fmt.Println("If there's an icon on your Desktop, you can double-click it to launch the GUI.")
}
