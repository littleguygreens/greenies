package main

// cmd_gui.go handles the "greenies gui" command.
//
// It starts a local web server so the grower can use the scheduler through
// their browser instead of (or alongside) the terminal. The server only
// listens on localhost — it's not visible to anyone else on the network.
//
// Usage:
//   greenies gui          → starts the server on port 8080
//   greenies gui 3000     → starts the server on a custom port
//
// The grower's browser opens automatically. Press Ctrl+C to stop.

import (
	"fmt"
	"os"
	"strconv"

	"github.com/littleguygreens/greenies/internal/gui"
)

// runGui starts the GUI web server.
func runGui() {
	// Default port is 8080. The grower can pass a different port number
	// as an argument if 8080 is already in use by something else.
	port := 8080
	if len(os.Args) >= 3 {
		p, err := strconv.Atoi(os.Args[2])
		if err != nil {
			fmt.Printf("Error: %q is not a valid port number\n", os.Args[2])
			os.Exit(1)
		}
		port = p
	}

	// Start the server. This call blocks (keeps running) until the user
	// presses Ctrl+C or the server hits a fatal error.
	if err := gui.StartServer(port, Version); err != nil {
		fmt.Printf("Server error: %v\n", err)
		os.Exit(1)
	}
}
