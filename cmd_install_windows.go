// Build tag: only compile this file on Windows. Go reads this line and skips
// the entire file when building for Linux, macOS, or any other operating system.
//go:build windows

package main

// cmd_install_windows.go handles the "greenies install-desktop" command on
// Windows.
//
// On Windows, no installer is needed. When greenies.exe is compiled with the
// -H=windowsgui flag, double-clicking it launches the GUI in the browser
// with no terminal window. The grower can pin it to the taskbar or Start
// Menu by right-clicking the .exe file.
//
// This file exists so the program compiles on Windows without a missing
// function error. It just prints a friendly explanation.
//
// Each operating system has its own version of this file:
//   cmd_install_linux.go   — .desktop file for Linux
//   cmd_install_darwin.go  — .app bundle for macOS
//   cmd_install_windows.go — friendly message (this file)

import "fmt"

// runInstallDesktop prints a message explaining that Windows doesn't need
// a separate install step.
func runInstallDesktop() {
	fmt.Println(`On Windows, no installation is needed!

Just double-click greenies.exe to launch the GUI in your browser.

To make it easier to find:
  - Right-click greenies.exe and choose "Pin to taskbar"
  - Or right-click and choose "Pin to Start" to add it to your Start Menu
  - Or drag it to your Desktop to create a shortcut

That's it — no installer required.`)
}
