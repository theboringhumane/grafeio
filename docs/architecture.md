# theboringoffice — Architecture ("the boring office") — v2 (Go)

A terminal office run by a real agent manager. Everything on screen is backed
by a real system; nothing on screen is fake except the coffee.

> v2 note: the UI is Go + [charmbracelet](https://charm.land) — bubbletea v2
> (alt-screen, mouse), bubbles v2, glamour v2, lipgloss v2. The Ink/Node v0.1
> app stays frozen v1 behaviors, kept as git tag `node-v0.1.0`.

```
+----------------- THEBORINGOFFICE (Go + Bubble Tea) ------------------+
|  topbar: theboringoffice <ver> | MODE | agents <n>   <clock> | <cwd> |
|  OFFICE FLOOR (left, flex)  |  SIDEBAR (right, cfg width 26..100)    |
|  sprites animated           <- tabs: chat | terminal | agents |      |
|  by live events                 board | mail | activity              |
|                             <- chat: you prompt the real boss        |
|                                terminal: a REAL PTY shell            |
|  statusbar: status | key hints | board p/i/d | mode                  |
+----------------------------------------------------------------------+
                                 |
        +------------------------+------------------------------+
  opencode serve (HTTP + SSE)              agentmemory server (3111)
  boss session / children / SSE events     actions (board) + signals (mail)

Floor physics (event -> sprite):
  task dispatched      -> employee gets up, walks to manager desk (meeting)
  message/part updates -> employee at desk, WORKING (typing frames)
  session idle         -> occasionally drifts to the tea machine
  result returns       -> walks back, drops mail in the tray
  permission ask       -> stands AT THE MAIL BOX waving (blocked)
```

## Where each piece lives

| Path | Job |
|---|---|
| `cmd/theboringoffice` | TUI entry: flags (`--demo`, `--server`, `--theme`, `--version`), spawns/attaches the backend, wires `app.SpawnTerminal = panels.NewTerminal`, runs the tea program |
| `cmd/headless` | verification binary for the backend layer: demo run, live spawn, `--prompt` round-trips, `--batch-probe` contract checks |
| `cmd/uishot` | deterministic UI shot harness: the REAL app model against a scripted stub backend, fixed size + event script, frame printed between markers |
| `cmd/floorshot` | freeze-frame renderer for the office floor: styled + plain frames of a seeded state at standard shell sizes |
| `cmd/termshot` | headless proof harness for `internal/term`: real PTY round-trip, grid asserts, zombie checks |
| `cmd/soundtest` | verification binary for the sound layer: play all / `--only` / `--bell-mode` / `--list` |
| `internal/app` | root bubbletea model: the state reducer, layout + key routing, boot splash, power governor, digest render cache, ambient social life, terminal-tab adapter |
| `internal/state` | the ONE contract backend and UI speak: `OfficeState`, `Event`, the `Backend` interface (incl. `MCPServers` / `ReconnectMCP`) |
| `internal/backend` | the two `state.Backend`s — scripted `demo.go`, live `opencode.go` (SSE client) — plus pure SSE normalization (`events.go`), the agentmemory adapter, MCP status/connect, question kinds |
| `internal/panels` | the sidebar: tab strip + chat, terminal, agents, board, mail, activity; slash/@ popover, question + permission modals, MCP status block, subagent threads |
| `internal/office` | the floor: props-driven floorplan, roster seats, sprite glyphs + walker physics, the pure frame renderer, tick-pure ambient fixtures |
| `internal/chrome` | topbar, statusbar, shared lipgloss styles + themes |
| `internal/config` | one file to run the office: `~/.theboringoffice/configs/brain.json` (created with defaults on first run; precedence CLI > brain.json > UI prefs > defaults) |
| `internal/charter` | the oikonomos manager protocol as an embedded asset, copied into `<dir>/.opencode/` for the spawned server |
| `internal/sound` | terminal-native office audio: eight pure-Go PCM chimes into `~/.theboringoffice/sounds/`, platform player / bell / off |
| `internal/term` | embedded OS terminal: real PTY session, xterm-style screen grid, scrollback |
| `internal/version` | build-time stamp, one source of truth: `dev` in-tree, releases rewrite the vars via ldflags `-X`; drives `theboringoffice --version` |

## Data flow

`opencode serve`'s SSE stream (`GET /event`) is decoded by pure helpers in
`internal/backend/events.go` — WHAT an event means for the office, no I/O;
`internal/backend/opencode.go` owns every network call. Backend goroutines
hand `state.Event`s to `tea.Program.Send`; the reducer in
`internal/app/model.go` folds each into the single `OfficeState`; the views —
the `internal/office` floor frame, the `internal/panels` sidebar tabs — render
purely from `(state, tick, size)`. UI never calls SDK/HTTP; backend never
renders. The demo backend speaks the SAME events on a timer chain; the
agentmemory HTTP adapter merges board + mail alongside SSE on a 5s sync.

## Testing discipline

Every feature keeps a colocated `*_test.go` beside the code it covers
(`office/floor_ambient_test.go`, `panels/chat_render_test.go`, …). Render
paths take NO goroutines or timers: frames are pure functions of an injected
synthetic tick — `BuildRows(state.OfficeState{Tick: 2}, 120, 26)` asserts that
exact blink/steam phase. The `cmd/{uishot,floorshot,termshot,headless}`
harnesses freeze deterministic full runs into greppable frames.

## Design vows

1. **One state shape.** UI never calls SDK/HTTP directly; backend never renders.
2. **Demo mode is first-class** (`theboringoffice --demo`): simulated events, alive on any machine — EXPLICITLY labeled demo.
3. **The boss is real.** Chat goes to a real opencode session with the oikonomos plugin active.
4. Plain ASCII in-app — emoji budget near zero (the 📎 attachment chip excepted).
