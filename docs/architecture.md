# Grafeio — Architecture (γραφείο, "the office") — v2 (Go)

A terminal office run by a real agent manager. Everything on screen is backed
by a real system; nothing on screen is fake except the coffee.

> v2 note: the UI is Go + [charmbracelet](https://charm.land) — bubbletea v2
> (alt-screen, mouse), bubbles v2 (viewport / textarea / spinner / help),
> glamour v2 (markdown), lipgloss v2 (theme). The Ink/Node v0.1 app is frozen
> v1 behaviors (same event contract, same zones/walkers; v1 app preserved
> as git tag `node-v0.1.0`).

```
+------------------------- GRAFEIO (Ink TUI) -------------------------+
|                                                                     |
|  OFFICE FLOOR (left, flexGrow) |  right sidebar: MAIL over BOARD   |
|  sprites animated          <- agentmemory      <- agentmemory       |
|  by live events               signals            actions frontier   |
|                                                                     |
|  CHAT (bottom center) — you talk to the boss (oikonomos manager)    |
|  -> prompt to opencode primary session                              |
+-------------------------------+------------------------------------+
                                |
        +-----------------------+-----------------------+
        |                                            |
  opencode serve (HTTP)                    agentmemory server (3111)
  sessions / children / SSE events         actions (board) + signals (mail)

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
| `src/index.ts` | entry: flags (`--demo`, `--server`, `--dir`), spawns/attaches backends, mounts Ink app |
| `src/app.tsx` | Ink layout grid: floor top, board right, mail left, chat bottom |
| `src/state.ts` | `OfficeState` — the ONE shape both backend and UI speak |
| `src/backend/events.ts` | normalizes opencode SSE + agentmemory polls into OfficeState updates |
| `src/backend/opencode.ts` | `opencode serve` spawn + `@opencode-ai/sdk` client (sessions, prompt, children, events) |
| `src/backend/agentmemory.ts` | HTTP adapter for actions/signals; degrades to in-memory demo backend when unreachable |
| `src/office/*` | the floor: desk map, sprite states, walker physics, frame renderer |
| `src/panels/*` | taskboard, mailbox, chatbox |

## Design vows

1. **One state shape.** UI never calls SDK/HTTP directly; backend never renders.
2. **Demo mode is first-class** (`grafeio --demo`): simulated dispatch events so
   the office is alive on any machine — and it is EXPLICITLY labeled demo.
3. **The boss is real.** Chat goes to a real opencode session with the
   oikonomos plugin active, so the manager actually manages.
4. Plain ASCII in-app (no emojis in the UI code wall).
