# Greenies

A microgreens farm scheduling tool. Plan crop cycles, track tray slots, log
harvests, and keep your whole grow operation visible — from the terminal or
a browser.

Greenies compiles to a **single binary** with no runtime dependencies. Copy it
to any Linux, macOS, or Windows machine and it just works.

## Features

- **Crop library** — define varieties in a CSV file (opens in Google Sheets or
  any spreadsheet app). Each row describes one day of a crop's lifecycle: stage,
  tasks, seed weight, medium, yield, and costing fields.
- **Cycle planner** — schedule a full grow cycle from a crop name and start date.
  Forward or backward planning, fixed-tray or yield-driven modes.
- **Calendar views** — day, week, and month views in the terminal or browser.
- **Farm visualizer** — live snapshot of every slot on the farm: what's growing,
  when it was planted, when harvest is due.
- **Conflict checker** — warns when a plan would exceed blackout or lit-rack
  capacity in any environment.
- **Harvest log** — record actual yield against expected, track performance over
  time.
- **Crop trialling** — run experimental grows with temporary parameters, log
  observations, compare trials side by side, promote winners to the main library.
- **Mid-cycle adjustments** — change parameters on a live tray without touching
  the crop template; schedule ripples forward automatically.
- **Profitability tracking** — per-crop costing (seed, medium, containers, labels)
  and revenue projections on the dashboard.
- **Browser GUI** — full-featured web interface with dark/light themes, swim-lane
  calendar, and all the same capabilities as the CLI.
- **Google integration** — two-way sync with Google Sheets (crop library, farm
  layout, supplies, trials, schedule, harvests) and one-way push to Google
  Calendar and Google Tasks.
- **Desktop shortcut** — `greenies install-desktop` creates a clickable launcher
  on Linux, macOS, or Windows.

## Requirements

- **Go 1.22 or later** to build from source.
- No other dependencies. The binary embeds all templates, CSS, and JavaScript.

## Install

```bash
# Clone the repository
git clone https://github.com/littleguygreens/greenies.git
cd greenies

# Build and install the binary into ~/go/bin/
go install .
```

Make sure `~/go/bin` is on your PATH. Then run `greenies` from anywhere.

## Quick start

```bash
# Launch the browser GUI (default when no command is given)
greenies

# Or use the CLI directly
greenies plan        # schedule a crop cycle
greenies list        # view your calendar
greenies snapshot    # see the live farm view
greenies crops       # browse or edit your crop library
greenies harvest     # log a completed harvest
```

On first run, Greenies creates a `~/.greenies/` folder with default data files.

## CLI commands

| Command                    | What it does                                      |
|----------------------------|---------------------------------------------------|
| `greenies` (no args)       | Launch the browser GUI                            |
| `greenies gui`             | Launch the browser GUI                            |
| `greenies list`            | Calendar view — week, month, or date range        |
| `greenies plan`            | Schedule a crop cycle interactively               |
| `greenies snapshot [date]` | Farm snapshot — slots, cycles, harvests, conflicts |
| `greenies crops`           | List crop varieties; add or edit a crop            |
| `greenies harvest`         | Log a completed harvest                           |
| `greenies harvestlog`      | View harvest history                              |
| `greenies trial`           | Start, manage, view, or compare crop trials       |
| `greenies adjust`          | Mid-cycle tray adjustments                        |
| `greenies delete`          | Delete a task or cycle                            |
| `greenies clear`           | Wipe all tasks (asks for confirmation)            |
| `greenies sync`            | Pull from or push to Google Sheets/Calendar       |
| `greenies install-desktop` | Create a desktop shortcut for the GUI             |
| `greenies help`            | Print usage information                           |

## Data files

All user data lives in `~/.greenies/`. Nothing is stored inside the project
folder itself.

| File              | Format | Purpose                                  |
|-------------------|--------|------------------------------------------|
| `crops.csv`       | CSV    | Crop library — one row per crop-day       |
| `farm.csv`        | CSV    | Farm layout — environments and capacities |
| `supplies.csv`    | CSV    | Supply costs and categories               |
| `tasks.json`      | JSON   | Calendar tasks (internal, not hand-edited)|
| `cycles.json`     | JSON   | Scheduled crop cycles                     |
| `harvests.json`   | JSON   | Harvest log                               |
| `trials.json`     | JSON   | Trial metadata and observations           |
| `trials.csv`      | CSV    | Trial data (regenerated from trials.json) |
| `config.json`     | JSON   | Program settings (internal)               |
| `credentials.json`| JSON   | Google OAuth credentials (gitignored)     |
| `token.json`      | JSON   | Google OAuth token (gitignored)           |

CSV files are designed to be edited in Google Sheets. JSON files are managed by
the program — you don't need to touch them by hand.

## Crop library

The crop library is a CSV file where each row is one day of a crop's lifecycle.
The template ships with two reference crops (sunnies and peas). To add your own:

1. Open `~/.greenies/crops.csv` in Google Sheets (or any spreadsheet app).
2. Add rows for your new variety following the same pattern.
3. Or use `greenies crops` in the terminal / the Crops page in the GUI.

Key columns: `name`, `day`, `stage`, `tasks`, `overnight_soak`, `soak_hours`,
`seed_grams`, `medium_litres`, `dark_days`, `light_days`, `yield_grams`,
`seed_cost`, `seed_purchase_weight`, `unit_weight`, `unit_sell_price`.

## Farm layout

The farm layout is defined in `~/.greenies/farm.csv`. Each row is one
environment:

```
name,type,capacity
blackout,blackout,100
main_tent,lit,64
test_tent,lit,16
grow_trays,inventory,150
bottom_trays,inventory,150
```

Edit this file to match your physical setup. You can have any number of lit
environments — the conflict checker tracks each one independently.

## Google Sheets and Calendar sync

Greenies can sync your data with a Google Sheet (two-way) and push tasks to
Google Calendar and Google Tasks.

No setup is needed — run `greenies sync` (or use the Sync page in the GUI)
and you'll be prompted to sign in with Google in your browser on first use.
Your sign-in token is stored privately at `~/.greenies/token.json`; nothing
about your Google account ever touches this repository.

The Google Sheet has eight tabs: Crops, Cycle, Farm, Supplies, Trials, Schedule
Tasks, Schedule Cycles, and Harvests.

### Using your own Google credentials (optional)

Out of the box, Greenies identifies itself to Google using OAuth credentials
for the shared Greenies Cloud project, embedded in the binary. Google treats
credentials for desktop apps as non-confidential (the real security is your
own Google sign-in), and embedding them is standard practice for open-source
tools — rclone does the same. But every user of the shared project draws on
one API quota, so if you'd rather use your own Google Cloud project, there
are two ways:

**Without rebuilding** — create a Google Cloud project, enable the Sheets,
Calendar, and Tasks APIs, create an OAuth "Desktop app" client, download its
`credentials.json`, and place it at `~/.greenies/credentials.json`. It takes
priority over the embedded credentials automatically.

**Baked into your own build** — if you compile from source, you can embed
your own credentials so your binary stays a single self-contained file:

```bash
go build -ldflags "\
  -X github.com/littleguygreens/greenies/internal/gcal.embeddedClientID=YOUR_CLIENT_ID \
  -X github.com/littleguygreens/greenies/internal/gcal.embeddedClientSecret=YOUR_CLIENT_SECRET"
```

Set both flags together — a client ID from one project with a secret from
another will fail to sign in.

## Android

An experimental Android wrapper lives in `desktop/android/`. It opens the
Greenies GUI in a WebView. See `desktop/android/build.sh` for build
instructions.

## Project structure

```
main.go                 Entry point and command routing
cmd_*.go                One file per CLI command
internal/
  task/                 Task data model
  store/                Load/save tasks.json
  calendar/             Day and week view rendering
  crop/                 Crop library, CSV parsing, profitability
  scheduler/            Cycle scheduling engine
  farm/                 Farm environment and cycle models
  visualizer/           Farm snapshot builder
  checker/              Slot conflict detection
  trial/                Crop trial management
  supply/               Supply cost tracking
  config/               Program settings
  export/               Exporter interface (console, future outputs)
  gcal/                 Google Calendar, Tasks, and Sheets integration
  gui/                  Browser GUI (server, handlers, templates, static assets)
```

## License

This project is licensed under the [GNU General Public License v3.0](LICENSE).
You are free to use, modify, and distribute this software. If you distribute a
modified version, you must also release your source code under the same license.
