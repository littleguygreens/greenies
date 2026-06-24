# Claude Session Instructions

## Who I Am
I am a complete beginner to programming and IDEs. I have no formal coding background.
I am building a **microgreens crop scheduling CLI tool in Go** as a personal project.
I am using **VS Code with Claude Code** on **Linux Mint**.

## How I Need You to Communicate
- Explain everything like I am 5 years old. Assume I know nothing about programming terminology.
- When you use a technical term for the first time, define it immediately in plain English.
- Never assume I know what an acronym means — spell it out the first time.
- Use real-world analogies wherever possible (e.g. the Stardew Valley crop calendar comparison).
- Keep explanations short and conversational. Do not write walls of text.

## How I Need You to Write Code
- Before writing any code, explain in plain English what you are about to do and why.
- Before choosing any external library, explain what it does, why you are choosing it over alternatives, and what its tradeoffs are.
- Never use a library without justifying it first.
- Write code in small, testable pieces rather than large blocks all at once.
- After writing code, explain what it does line by line if it introduces new concepts.
- **Comment the code extensively.** This codebase should be readable by someone with
  no programming background. Every function (a reusable block of code that does one
  job), every data structure (a container that holds related information), and every
  non-obvious decision should have a plain-English comment explaining what it is,
  what it does, and — most importantly — *why* it exists.
- Comments should explain intent, not just restate the code. For example:
  - Bad comment: `// add 1 to the counter`
  - Good comment: `// each tray needs a unique ID number so we can tell them apart
    even if they contain the same crop variety`
- Write comments as if the reader is a curious small business owner who wants to
  understand and eventually modify the program, not as if the reader is a developer.

## Core Philosophy
This tool should embody the same minimalism as the farm it serves.
- **Keep the codebase as small as possible.** If a problem can be solved with 10 lines
  of standard library code, do not reach for an external library to do it in 2.
- Every external dependency (a library written by someone else that we add to the
  project) must justify its existence. If in doubt, leave it out.
- The program will run on an old, low-powered laptop. Optimise for fast startup,
  low memory usage, and minimal resource consumption at all times.
- Prefer simple, readable code over clever code. If a solution is hard to explain
  in plain English, it is probably too complicated.
- When there are two valid approaches, always choose the smaller and simpler one.
- **Portability is a core requirement.** One of the reasons Go was chosen is that it
  compiles to a single self-contained binary (one file that contains the entire program,
  with no external dependencies required on the machine running it). Never make
  architectural decisions that compromise this — the finished program should be movable
  to any computer by copying a single file.
- **The long-term goal is to publish this as open source software** (software whose
  code is publicly available for anyone to read, use, and contribute to). Plan for
  this from the start:
  - Never hardcode personal information (my name, farm name, email address, file
    paths specific to my machine) directly into the code. These should always live
    in a separate configuration file that each user customises for themselves.
  - Never hardcode secrets (passwords, API keys — the private codes that grant access
    to services like Google Calendar) into the code. These must always be stored
    separately and never committed to Git where they would become publicly visible.
  - The program should work out of the box for a new user with sensible defaults —
    a first-time setup should require minimal configuration to get started.
  - Remind me to add a LICENSE file and a README file (a plain-English introduction
    to the project for anyone who finds it) before the project is made public.

## Project Context
- The core tool is a CLI (Command Line Interface — a program you control by typing
  commands in a terminal rather than clicking buttons) for scheduling microgreens
  crop cycles.
- The architecture should be designed for extensibility — future phases will add
  output destinations (e.g. Google Calendar, CSV export, email) and notification
  channels (e.g. Slack, SMS). Use interfaces to keep these decoupled from the core
  scheduler.
- **Crop parameters are stored in a human-readable CSV file** (a plain text file
  that opens in any spreadsheet program). The intended human editor for this file
  is **Google Sheets** (free, browser-based, no installation required, works on
  any device). A grower should be able to add or edit a crop variety by editing
  a row in their spreadsheet — no programming knowledge required.
- **Architecture decision to make in Phase 2:** decide whether the program reads
  a locally exported CSV file, or syncs directly with Google Sheets via its API.
  Both approaches have tradeoffs to discuss when we get there.
- JSON must never be used for any file that a human is expected to read or edit.
- Target: Go 1.22+, standard library preferred, no unnecessary dependencies.

## Git Workflow
- I am a complete beginner to Git. Explain every Git action in plain English before
  asking me to run it in the terminal.
- At logical checkpoints in development, remind me to commit my work and suggest a
  plain-English commit message that describes what was just built.
- Never assume I know Git terminology. Define terms like branch, commit, push, pull,
  merge, and repository the first time they appear.
- Prefer simple, linear Git workflows — do not introduce branching strategies or
  advanced Git concepts until the basics are solid.

## Version Number
- The current version lives in `version.go` at the root of the project as `const Version`.
- **Before every commit that will become a new release, update `Version` in `version.go`
  to match the git tag that will be pushed.** For example, if the tag will be `v1.2.0`,
  set `const Version = "v1.2.0"` before committing.
- The in-app updater compares this string against the latest GitHub release tag. If they
  don't match, the update checker will always show a false "update available" or "up to date"
  result. Keeping them in sync is the only maintenance the updater needs.

## Reminders for Claude
- If you are about to make an architectural decision, explain it in plain English
  before implementing it — do not just write code and explain afterward.
- If there are multiple valid approaches to something, briefly describe the options
  and ask which direction I prefer before proceeding.
- Do not truncate code. Always write complete files.
- Remind me to commit my work to Git at logical checkpoints in development.
- **At the end of every phase:** run `/phase-review` before declaring the phase
  complete. Any blocking issues found must be resolved before Phase N+1 begins.
  Any architectural notes for future phases should be added to the relevant phase
  entry in the Roadmap section below.

---

## Farm Domain Knowledge

This section describes the physical reality of the farm. Use it whenever making
decisions about the data model. Never assume anything about the farm that is not
written here.

### Terminology
- **Grow tray** — a tray with drilled holes. The crop lives in this tray for its
  entire lifecycle from sowing through harvest.
- **Bottom tray** — a solid tray with no holes. Paired with a grow tray during
  germ and blackout only, used for an irrigation practice called bottom watering.
  Returned to inventory the moment the grow tray moves to light.
- **Tray pair** — one grow tray nestled into one bottom tray. Exists only during
  the germ and blackout stages.

### The Three Stages of a Crop Lifecycle
Every crop passes through some or all of these stages in order:

1. **Germ (Germination)** — begins on Day 0 or Day 1 depending on the crop.
   Some crops require an overnight seed soak on Day 0 before trays are prepared.
   During Day 0, no trays are consumed from inventory and no slots are occupied.
   Trays enter blackout slots as soon as seed is sown.

2. **Blackout** — grow trays (in tray pairs) occupy blackout slots. Daily tasks
   vary by crop variety and may include spraying, bottom watering, rotating, and
   stacking or unstacking. The number of blackout days varies by crop.

3. **Light** — grow trays occupy lit rack slots. The bottom tray is returned to
   inventory at the moment of the move-to-light task — this is tied to the event,
   not a fixed day number. Watering is automated on lit racks, so some crops have
   do-nothing days where no tasks are required. The grow tray is returned to
   inventory at harvest.

### Slot Accounting Rules
- One blackout slot is reserved per grow tray from the moment trays are sown,
  regardless of whether trays are physically stacked in bundles.
- Stacked bundles (e.g. 4 tray pairs per bundle) have a smaller physical
  footprint but watering requires room to move — always count one slot per
  grow tray, no exceptions.
- One lit rack slot is occupied per grow tray from move-to-light until harvest.
- Day 0 soak crops occupy zero slots and consume zero inventory until Day 1.

### Inventory Tracked
The farm visualizer tracks the following physical inventory. The system must be
extensible — new item types should be addable without rewriting existing code:
- **Grow trays** — in circulation (on the farm) or available (in inventory)
- **Bottom trays** — in circulation (paired in blackout) or available (in inventory)
- **Seeds** — quantity on hand per crop variety
- **Grow medium** (dirt) — quantity on hand
- **Foodservice containers** — and any other consumables to be added later

### Physical Farm Layout

The farm consists of one shared blackout room and multiple lit environments.
Each environment is independently configured and tracks its own slot usage.
The layout is defined in a configuration file — never hardcoded.

**Blackout room**
- Single shared space used by all crops regardless of destination lit environment
- Slot capacity: to be confirmed (treat as effectively unlimited for now)
- The capacity field must exist in config from day one, ready to be filled in

**Main tent**
- 4 racks × 4 shelves × 4 slots per shelf = 64 lit slots total

**Test tent**
- 1 rack × 16 slots = 16 lit slots total

### Rules for Multiple Lit Environments
- A crop cycle is assigned to a specific lit environment at the moment it moves
  to light — not before. Blackout is environment-agnostic.
- Each lit environment tracks its own slot occupancy independently.
- The conflict checker must check slot availability per environment, not across
  the farm as a whole.
- The number of lit environments is variable — the system must support any number
  of named environments, each with their own rack/shelf/slot configuration.
- Environment conditions (temperature, humidity) do not affect crop parameters
  or scheduling logic — environments are purely physical spaces for slot tracking.

### Reference Crop Cycles

**Sunnies** (9-day cycle, no Day 0 soak)

| Day | Stage   | Tasks |
|-----|---------|-------|
| 1   | sow     | Measure seed, soak 4 hours, drain seed, prepare trays, sow seed, saturate dirt, top water, stack |
| 2   | dark    | Top water, rotate stack |
| 3   | dark    | Top water, rotate stack |
| 4   | dark    | Bottom water, top water, rotate stack |
| 5   | dark    | Bottom water, top water, unstack |
| 6   | light   | Move to lit rack |
| 7   | light   | Mist, dehusk |
| 8   | light   | Mist, dehusk |
| 9   | harvest | Harvest, wash |

**Pea** (8-day cycle, Day 0 overnight soak)

| Day | Stage   | Tasks |
|-----|---------|-------|
| 0   | sow     | Measure seed, soak overnight (no slots occupied, no inventory consumed) |
| 1   | sow     | Drain seed, prepare trays, sow seed, saturate dirt, top water |
| 2   | dark    | Bottom water |
| 3   | dark    | Bottom water |
| 4   | dark    | Bottom water |
| 5   | light   | Move to lit rack |
| 6   | light   | (no tasks — watering is automated on lit racks) |
| 7   | light   | (no tasks — watering is automated on lit racks) |
| 8   | harvest | Harvest |

### Key Scheduling Rules
- The bottom tray return event is always tied to the move-to-light task, never
  to a fixed day number.
- Do-nothing days are valid and expected — the scheduler must never assume every
  day in a cycle has at least one task.
- Day 0 is a valid cycle day for some crops. It occupies no physical resources
  but should appear on the calendar as a reminder to soak seed.
- All per-crop parameters (cycle length, tasks per day, dirt quantity, soak
  duration) are configurable per variety and must never be hardcoded.

---

## Roadmap

### Phases 1–8 ✅ COMPLETE (shipped in v1.0 and v1.1)

- **Phase 1** — Text-based calendar, day/week view, add/edit/delete tasks, data persisted to `~/.greenies/tasks.json`, Exporter interface stubbed
- **Phase 2** — Crop library in CSV (editable via Google Sheets), full crop cycle scheduling, four planning modes (forward/backward, fixed trays/yield-driven)
- **Phase 3** — Farm visualizer: configurable shelves and slots, live text view of every tray slot
- **Phase 4** — Conflict checker: tray capacity and timing alerts, schedule ripple awareness, harvest log
- **Phase 5** — Google Calendar integration: OAuth2 browser-based auth, export crop cycles and tasks as calendar events
- **Phase 6** — Crop trialing: temporary parameters, observation log per trial tray, promote or discard
- **Phase 7** — Mid-cycle tray adjustments: edit individual active trays, automatic schedule ripple-forward, conflict checker re-runs
- **Phase 8** — Browser-based GUI (DayPilot Lite Gantt calendar, farm visualizer, crop library, active trays); CLI remains fully functional

### Phase 9 — Future Features (optional)
- **Individually addressable slot records** — track which specific physical slot
  (rack, shelf, position) each tray occupies; required before a GUI can show a
  true map of the farm
- **Discord integration** — standalone bot that reads from the scheduler's local
  data files; daily task summary, task logging, harvest notifications
- **Cross-platform desktop shortcuts** — `greenies install-desktop` currently
  targets Linux only (`.desktop` file). Add support for macOS (`.app` bundle)
  and Windows (GUI-mode build + shortcut creation) so the program can be
  launched by double-clicking an icon on any OS, with no terminal window
- **Embedded Google OAuth credentials** — embed the OAuth client ID and secret
  directly in the binary so new users can sign in with one click instead of
  manually creating a Google Cloud project and downloading credentials.json.
  This is standard practice for open-source desktop apps (e.g. rclone) —
  Google treats desktop client secrets as non-confidential. Tradeoffs to
  consider before enabling: all users share one API quota (could hit limits
  if the tool gets popular), abuse by third parties could get the Cloud
  project suspended, and Google requires a verification review before more
  than 100 users can authorise. Best to revisit once the tool is published
  and user count is known.
