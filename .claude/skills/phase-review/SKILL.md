---
name: phase-review
description: Full project code review at the end of a development phase. Reads every source file and reviews the whole codebase for quality, cohesiveness, and alignment with the project's guiding principles.
---

You are reviewing the greenies project — a microgreens crop scheduling CLI tool in Go,
built by a complete beginner. This review has two parts: first the changes from the
latest phase, then the whole project. Complete both before giving your summary.

## Step 1 — Read everything

Read all Go source files in the project:
- main.go
- internal/task/task.go
- internal/store/store.go
- internal/calendar/calendar.go
- internal/export/exporter.go
- internal/export/console.go
- Any other .go files that exist

Also read CLAUDE.md to remind yourself of the project's guiding principles.

Then run `git log --oneline` to see the commit history, and `git diff <previous-phase-commit>..HEAD`
to see exactly what changed in the latest phase. If this is the first review, diff against
the very first commit.

---

## Part 1 — Review of the latest phase changes

Before looking at the whole project, focus only on what was added or changed in this phase.

### What was built
Summarise in plain English what this phase added to the project.

### Phase-specific review
- Does the new code follow the same commenting style as the rest of the project?
- Are there any shortcuts taken that should be cleaned up before moving on?
- Does the new code introduce any external dependencies that weren't justified?
- Are there any obvious bugs or edge cases that weren't handled?

---

## Part 2 — Whole-project review

Now review the entire codebase as a whole. Apply each criterion to all files, not just
the new ones.

## Step 2 — Review against these criteria

For each criterion, give a clear verdict (Good / Needs attention / Issue found) and
specific examples from the code where relevant.

### 1. Comments — plain English for a beginner
Every function, struct, and non-obvious decision should have a comment written as if
the reader is a curious small business owner, not a developer.
- Are all functions commented?
- Do comments explain *why*, not just *what*?
- Would a non-programmer understand what each piece does?

### 2. Hardcoded values
Nothing personal (names, paths, emails) or configurable (farm layout, crop parameters)
should be baked into the code.
- Are file paths constructed dynamically (e.g. os.UserHomeDir) rather than hardcoded?
- Are any magic numbers or strings that belong in a config sitting in the code?

### 3. Simplicity — is the code as small as it could be?
- Is any logic more complicated than it needs to be?
- Are there any external libraries that weren't justified?
- Could any function be simplified without losing clarity?

### 4. Architecture — does it still fit the roadmap?
Cross-check the code against the phased roadmap in CLAUDE.md:
- Is the Exporter interface in place and genuinely decoupled from the calendar logic?
- Is the Task struct still a clean fit for Google Calendar events (Phase 5)?
- Is there anything in the current code that would make a future phase harder to add?
- Is there anything missing that the roadmap says should already be here?

### 5. Cohesiveness — does the whole project hang together?
- Do the packages feel like they belong to the same project, or are there inconsistencies
  in naming, style, or approach between files?
- Is the error handling consistent across the codebase?
- Are there any loose ends — dead code, unused variables, TODOs that need addressing?

### 6. Open source readiness
- Is there any personal information (name, farm name, email, machine-specific paths)
  in the code that would need removing before publishing?
- Is there a clear separation between code (committed to Git) and data/config (not committed)?

## Step 3 — Summary

End your review with:

**Phase verdict:** one sentence on the quality of what was just built.

**Overall verdict:** one sentence summarising the state of the whole codebase.

**Before starting the next phase:**
A short numbered list of any issues that should be fixed *before* moving on.
If there are none, say so explicitly.

**Things to watch in future phases:**
A short list of patterns or risks to keep an eye on as the project grows.
