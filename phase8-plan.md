# Phase 8: Browser-Based GUI

## Context

The CLI tool is feature-complete through Phase 7. Every command works by typing in the terminal. Phase 8 adds a browser-based GUI so a user can do everything by clicking buttons and filling in forms instead of typing commands. The CLI stays fully functional — the GUI is just an alternative way to use the same tool.

**The big idea:** `greenies gui` starts a tiny web server on your computer. You open your browser to `localhost:8080` and see a dashboard with pages for every command. No internet connection needed — it all runs locally.

---

## Key Decisions

### 1. How the GUI works (htmx + Go templates)

The Go program acts as both the web server and the page builder. When you click a button, the browser sends a request to the Go server, which does the work and sends back a chunk of HTML to display. A small JavaScript library called **htmx** (~50kb, one file) handles swapping new HTML into the page without reloading — so it feels snappy.

- **No React, no npm, no build tools.** The entire frontend is HTML files with Go template tags.
- **htmx is embedded in the binary** — downloaded once, committed to the repo. No CDN or internet needed at runtime.
- **Go's built-in `html/template` package** renders all pages. Standard library, no new dependencies.

### 2. DayPilot Lite — deferred to Phase 8b

The CLAUDE.md roadmap mentions DayPilot Lite for a Gantt-style calendar view (crop varieties as rows, time as columns, stage blocks as bars). It's a great visual but adds ~200kb of JavaScript and requires writing real JS code to feed it data. **Build the GUI without it first.** The initial calendar view uses simple HTML tables. DayPilot can be added later as a single enhancement page.

### 3. Single binary preserved

All HTML templates, CSS, and the htmx JS file are **embedded** into the Go binary using Go's `embed` package. The binary remains one portable file — copy it to any machine and it works.

---

## What Refactoring Is Needed (Minimal)

Only **two tiny changes** before GUI work begins:

1. **`internal/calendar/calendar.go`** — Export the private `tasksForDate` function by capitalizing it to `TasksForDate`. This lets the GUI handlers call it to get tasks for a specific date. One-line rename; the existing `PrintDay`/`PrintRange` functions call it internally already.

2. **`internal/visualizer/visualizer.go`** — No changes for MVP. `SnapshotText()` already returns a string that the GUI can display in a `<pre>` block. Optionally add a `SnapshotData()` function later for richer HTML rendering.

Everything else (`store`, `scheduler`, `crop`, `farm`, `trial`, `checker`) is already GUI-ready — they return clean data with no terminal I/O mixed in.

---

## New Files

```
internal/gui/
    gui.go              ← StartServer(), route registration, embed directives, template helpers
    handlers.go         ← One HTTP handler function per page/action
    static/
        htmx.min.js     ← htmx library (committed, embedded in binary)
        style.css       ← Simple stylesheet (embedded)
    templates/
        layout.html     ← Base layout: nav bar + content area, wrapped around every page
        dashboard.html  ← Home page: today's snapshot + today's tasks
        snapshot.html   ← Farm snapshot (date picker)
        list.html       ← Calendar view (week/month/range toggle)
        crops.html      ← Crop library table
        plan.html       ← Plan form: crop dropdown, trays, date, env, repeat
        plan_preview.html  ← htmx fragment: schedule preview + confirm button
        delete.html     ← Task list with delete buttons per row
        harvest.html    ← Eligible harvests with logging forms
        harvestlog.html ← Harvest history table
        trial.html      ← Trial dashboard: active, new, manage, view, compare
        adjust.html     ← Cycle list + adjustment form with preview
        adjust_preview.html ← htmx fragment: before/after preview
        sync.html       ← Sync button + status display
        clear.html      ← Confirmation page

cmd_gui.go              ← runGui() — starts the server, opens browser
```

**Estimated size:** ~800-1200 lines of Go, ~15-20kb of HTML templates, ~5kb CSS.

---

## How CLI Commands Map to GUI Pages

| CLI Command | GUI Page | How It Works |
|---|---|---|
| `greenies snapshot` | `GET /snapshot` | Load envs + cycles, render `SnapshotText()` in `<pre>`. Date picker for other dates. |
| `greenies list` | `GET /list` | Load tasks, group by date, render as day cards. Week/month/range toggle. |
| `greenies crops` | `GET /crops` | Load crop library, render as HTML table. |
| `greenies harvestlog` | `GET /harvestlog` | Load harvests, render as table (most recent first). |
| `greenies plan` | `GET /plan` + `POST /plan/preview` + `POST /plan/confirm` | All inputs visible in one form. "Preview" shows schedule below the form (htmx swap). "Confirm" saves it. |
| `greenies delete` | `GET /delete` + `POST /delete` | Task list with delete buttons. htmx removes the row on click. Task-vs-cycle choice shown inline. |
| `greenies clear` | `POST /clear` | Confirmation button. Wipes all tasks. |
| `greenies harvest` | `GET /harvest` + `POST /harvest` | Shows eligible cycles. Click "Log" → form expands for actual trays/grams. |
| `greenies trial` | `GET /trial` + `POST /trial/*` | Dashboard with active trials, buttons for new/manage/view/compare. Each action has its own form section. |
| `greenies adjust` | `GET /adjust` + `POST /adjust/preview` + `POST /adjust/confirm` | Pick cycle, pick anchor, fill adjustment form, preview before/after, confirm. Repeat by making another adjustment on the same page. |
| `greenies sync` | `POST /sync` | One button. Shows sync progress/result. |

**The key simplification:** Multi-step CLI prompts become single-page forms. In `plan`, instead of answering 6 questions one at a time, the grower sees all fields at once — crop dropdown, tray count, date picker, etc. — fills them in any order, then clicks "Preview." Much easier than the terminal.

---

## Endpoints

```
GET  /                     → dashboard
GET  /snapshot             → farm snapshot (optional ?date=YYYY-MM-DD)
GET  /list                 → calendar view (?mode=week|month, ?start=, ?end=)
GET  /crops                → crop library table
GET  /harvestlog           → harvest history
GET  /plan                 → plan form
POST /plan/preview         → returns preview HTML fragment (htmx)
POST /plan/confirm         → saves plan, returns success message
GET  /delete               → task list with delete buttons
POST /delete               → deletes task or cycle, returns updated list
POST /clear                → clears all tasks
GET  /harvest              → eligible harvests
POST /harvest              → logs harvest
GET  /trial                → trial dashboard
POST /trial/new            → creates new trial
POST /trial/manage         → manages existing trial
GET  /adjust               → active/upcoming cycles
POST /adjust/preview       → before/after preview (htmx)
POST /adjust/confirm       → applies adjustment
POST /sync                 → triggers Google Calendar sync
GET  /static/*             → embedded CSS + JS files
```

---

## Implementation Steps (Build Order)

Each step produces something visible and testable.

### Step 1: Minimal refactoring
- Export `tasksForDate` → `TasksForDate` in `internal/calendar/calendar.go`
- Update the one internal call site

### Step 2: Skeleton + Dashboard
- Create `internal/gui/gui.go`: `StartServer(port int)`, embed directives, template loading, route registration
- Create `cmd_gui.go`: `runGui()` that calls `gui.StartServer(8080)` and opens browser with `xdg-open`
- Add `case "gui":` to `main.go` switch
- Create `layout.html` (nav bar + content slot), `style.css`, embed `htmx.min.js`
- Create `dashboard.html`: show today's snapshot text + today's tasks
- **Test:** `greenies gui` → browser opens → dashboard shows farm status

### Step 3: Read-only views
- `/snapshot` — date picker + `SnapshotText()` in `<pre>`
- `/list` — week/month toggle, day cards with tasks
- `/crops` — HTML table of crop varieties
- `/harvestlog` — harvest history table
- **Test:** Click through all four pages, verify data matches CLI output

### Step 4: Simple actions
- `/delete` — task list with delete buttons (htmx row removal)
- `/clear` — confirmation page
- **Test:** Delete a task in browser, verify it's gone in `greenies list`

### Step 5: Plan command
- Plan form with all inputs: crop dropdown, trays, direction (sow/harvest), date, lit env, repeats
- `POST /plan/preview` → calls `scheduler.Schedule()`/`ScheduleForward()`, renders preview fragment
- `POST /plan/confirm` → saves tasks + cycle records
- Handle conflict checker warnings in preview
- **Test:** Plan a crop in the browser, verify it shows up in `greenies list` and `greenies snapshot`

### Step 6: Harvest logging
- Show eligible cycles (unharvested, within 30-day window)
- Expandable form per cycle for actual trays/grams
- **Test:** Log a harvest in browser, verify in `greenies harvestlog`

### Step 7: Trial management
- Active trials list, new trial form, manage (observe/outcome), view, compare
- **Test:** Create and manage a trial through the browser

### Step 8: Adjust command
- Cycle picker, anchor selection, adjustment form with side-by-side preview
- Cascade and crops.csv propagation options in the confirm step
- **Test:** Adjust a cycle in browser, verify updated schedule

### Step 9: Sync
- Single "Sync to Google" button
- Show sync result (success/error message)
- **Test:** Click sync, verify events appear in Google Calendar

### Step 10: Polish
- HTML5 `<input type="date">` for all date fields
- Mobile-friendly CSS (the tool might be used on a phone in the greenhouse)
- Error messages styled consistently
- `printUsage()` updated to include `greenies gui`

---

## Architecture Notes

- **Server binds to `127.0.0.1:8080` only** — localhost, not exposed to the network. No authentication needed.
- **No client-side state.** Every page load reads fresh data from disk. CLI and GUI can be used interchangeably — just refresh the browser.
- **Auto-open browser:** `xdg-open http://localhost:8080` on Linux. Falls back to printing the URL if it fails.
- **Graceful shutdown:** Ctrl+C in the terminal stops the server cleanly.
- **Concurrent access:** Both CLI and GUI read/write the same `~/.greenies/` files. For a single-user tool this is fine — just refresh the browser after using a CLI command.

---

## Critical Files to Modify

- [main.go](main.go) — add `case "gui":` (2 lines)
- [internal/calendar/calendar.go](internal/calendar/calendar.go) — export `tasksForDate` → `TasksForDate`

## Critical Files to Reference During Implementation

- [cmd_plan.go](cmd_plan.go) — the plan workflow logic to replicate in GUI handlers
- [cmd_adjust.go](cmd_adjust.go) — the adjust workflow logic
- [cmd_trial.go](cmd_trial.go) — the trial workflow logic
- [cmd_snapshot.go](cmd_snapshot.go) — snapshot rendering calls
- [internal/visualizer/visualizer.go](internal/visualizer/visualizer.go) — `SnapshotText()`, `CalendarTitle()`
- [internal/scheduler/scheduler.go](internal/scheduler/scheduler.go) — `Schedule()`, `ScheduleForward()`, `ScheduleFromDay()`

---

## Verification

After each step, test by:
1. Run `greenies gui` — server starts, browser opens
2. Click through the relevant page
3. Run the equivalent CLI command to verify data matches
4. For write operations: make a change in the GUI, verify with CLI (and vice versa)
5. After all steps: run the full CLI test suite (all commands) to confirm nothing broke
