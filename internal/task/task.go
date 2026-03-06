// Package task defines what a single calendar entry looks like.
//
// Think of this file as designing the columns in a spreadsheet row.
// Every item that appears on the calendar — "sow sunflowers", "water peas",
// a reminder, anything — is represented as one Task value.
//
// The fields below were chosen to match Google Calendar's event format
// exactly. That means in a future phase, converting a Task into a Google
// Calendar event will be straightforward — the information is already in
// the right shape.
package task

import (
	"crypto/rand"  // Go's built-in secure random number generator
	"encoding/hex" // converts random bytes into a readable string of letters and numbers
	"time"         // Go's built-in package for working with dates and times
)

// DateFormat is the single source of truth for how all dates are stored and
// displayed throughout the program. The format "2006-01-02" is Go's way of
// expressing YYYY-MM-DD — Go uses this specific reference date as a template
// rather than letters like Y, M, D. Odd, but that's just how Go works.
//
// Defined here in the task package because Task.Date is the field that owns
// this format — every other package that deals with dates borrows it from here.
const DateFormat = "2006-01-02"

// Task represents a single item on the calendar.
//
// Each field maps to a Google Calendar event field (noted in the comments),
// so this struct is ready for Phase 5 integration without any redesign.
type Task struct {
	// ID is a unique identifier for this task — like a serial number on a
	// product. No two tasks will ever share the same ID, even if they have
	// the same title and date. This lets us find, edit, or delete the exact
	// right task without any ambiguity.
	//
	// Maps to: Google Calendar event "id" field.
	ID string

	// Title is the short label shown on the calendar — e.g. "Sow sunflowers"
	// or "Move peas to light". Keep it brief; use Notes for detail.
	//
	// Maps to: Google Calendar event "summary" field.
	Title string

	// Date is the calendar day this task belongs to, stored as a plain string
	// in the format YYYY-MM-DD (e.g. "2026-03-05"). Using a string here keeps
	// the stored data simple and human-readable in the JSON file.
	//
	// Maps to: Google Calendar event "start.date" field (all-day event format).
	Date string

	// Notes is optional extra detail about the task — e.g. "2 trays, main tent"
	// or "check soil moisture before watering". Can be left blank.
	//
	// Maps to: Google Calendar event "description" field.
	Notes string

	// CreatedAt records the exact moment this task was first created.
	// Useful for sorting tasks that share the same date, and for a future
	// harvest log that needs accurate timestamps.
	CreatedAt time.Time

	// UpdatedAt records the last time this task was edited. Starts equal to
	// CreatedAt and is bumped every time the task is saved after a change.
	UpdatedAt time.Time
}

// New creates a brand-new Task with a unique ID and the current timestamp.
//
// Think of this like filling in the top of a form: the ID and timestamps are
// stamped automatically so you only need to supply the actual content.
//
// Usage:
//
//	t := task.New("Sow sunflowers", "2026-03-05", "2 trays, main tent")
func New(title, date, notes string) (Task, error) {
	id, err := generateID()
	if err != nil {
		// If we can't generate a random ID (very unlikely — this would mean
		// the operating system's random number source has failed), we return
		// an error so the caller can decide what to do rather than silently
		// creating a task with a broken ID.
		return Task{}, err
	}

	now := time.Now()

	return Task{
		ID:        id,
		Title:     title,
		Date:      date,
		Notes:     notes,
		CreatedAt: now,
		UpdatedAt: now,
	}, nil
}

// generateID creates a short, unique identifier string.
//
// It reads 8 random bytes from the operating system's secure random source
// and converts them to a 16-character string of letters and numbers
// (e.g. "a3f2c81b9d047e56"). This is short enough to type but random enough
// that two IDs will never collide in practice.
func generateID() (string, error) {
	// Make a slice (a variable-length list) to hold 8 random bytes.
	// 8 bytes gives us 16 hex characters — short and unique enough for our needs.
	bytes := make([]byte, 8)

	// Ask the operating system for random bytes. This is more reliable than
	// a simple counter because it works correctly even if two tasks are
	// created at the exact same millisecond.
	_, err := rand.Read(bytes)
	if err != nil {
		return "", err
	}

	// Convert the raw bytes into a readable string of hex characters
	// (hex uses the characters 0-9 and a-f, like colour codes in web design).
	return hex.EncodeToString(bytes), nil
}
