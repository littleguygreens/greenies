// Package store handles saving and loading tasks to and from disk.
//
// Think of this package as the filing cabinet for the program. When you add,
// edit, or delete a task, the store writes the updated list to a file so
// nothing is lost when the program closes. When the program starts, the store
// reads that file back in.
//
// The data file lives at ~/.greenies/tasks.json — inside a hidden folder in
// your home directory, separate from the code. It is listed in .gitignore so
// it is never accidentally uploaded to GitHub.
//
// Why JSON for this file?
// JSON is used here because it is the internal data format — it is never meant
// to be edited by hand. Any file a grower would edit (like the crop library)
// will use CSV instead, which opens cleanly in Google Sheets.
package store

import (
	"encoding/json" // Go's built-in package for reading and writing JSON data
	"fmt"
	"os"            // Go's built-in package for working with files and folders
	"path/filepath" // helps build file paths correctly across different operating systems

	"github.com/littleguygreens/greenies/internal/task"
)

// dataDir returns the path to the folder where all greenies data is stored.
// On Linux this will be something like: /home/farm/.greenies
//
// Using os.UserHomeDir() instead of hardcoding "/home/farm" means the program
// will work correctly for any user on any machine — a requirement for
// open-source distribution.
func dataDir() (string, error) {
	home, err := os.UserHomeDir()
	if err != nil {
		return "", fmt.Errorf("could not find home directory: %w", err)
	}
	return filepath.Join(home, ".greenies"), nil
}

// dataFile returns the full path to the tasks JSON file.
// Example: /home/farm/.greenies/tasks.json
func dataFile() (string, error) {
	dir, err := dataDir()
	if err != nil {
		return "", err
	}
	return filepath.Join(dir, "tasks.json"), nil
}

// Load reads all saved tasks from disk and returns them as a list.
//
// If the data file does not exist yet (e.g. on first run), Load treats that
// as "no tasks saved" and returns an empty list rather than an error.
// This means the program works correctly straight out of the box with no
// setup required.
func Load() ([]task.Task, error) {
	path, err := dataFile()
	if err != nil {
		return nil, err
	}

	// Read the raw contents of the file into memory.
	data, err := os.ReadFile(path)
	if err != nil {
		// os.IsNotExist checks specifically for "file not found" —
		// we treat that as an empty task list, not a real error.
		if os.IsNotExist(err) {
			return []task.Task{}, nil
		}
		return nil, fmt.Errorf("could not read tasks file: %w", err)
	}

	// Decode the JSON data into a slice (list) of Task values.
	// json.Unmarshal is like translating text on a page into structured data
	// the program can work with.
	var tasks []task.Task
	if err := json.Unmarshal(data, &tasks); err != nil {
		return nil, fmt.Errorf("tasks file appears to be corrupted: %w", err)
	}

	return tasks, nil
}

// Save writes the full list of tasks to disk, replacing whatever was there before.
//
// This is a full overwrite every time — we always write the complete list.
// For the number of tasks a single farm will ever have, this is fast and simple.
// If the data directory does not exist yet, Save creates it automatically.
func Save(tasks []task.Task) error {
	dir, err := dataDir()
	if err != nil {
		return err
	}

	// Create the ~/.greenies directory if it does not already exist.
	// os.MkdirAll will not complain if the directory is already there.
	// 0755 is a Unix permission code meaning: owner can read/write/enter,
	// everyone else can only read and enter (standard for directories).
	if err := os.MkdirAll(dir, 0755); err != nil {
		return fmt.Errorf("could not create data directory: %w", err)
	}

	path, err := dataFile()
	if err != nil {
		return err
	}

	// Encode the list of tasks into JSON format.
	// json.MarshalIndent adds line breaks and indentation so the file is
	// readable if someone opens it in a text editor.
	data, err := json.MarshalIndent(tasks, "", "  ")
	if err != nil {
		return fmt.Errorf("could not encode tasks: %w", err)
	}

	// Write the data to the file.
	// 0644 is a Unix permission code meaning: owner can read/write,
	// everyone else can only read (standard for data files).
	if err := os.WriteFile(path, data, 0644); err != nil {
		return fmt.Errorf("could not write tasks file: %w", err)
	}

	return nil
}
