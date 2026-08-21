# Grafeio (γραφείο)

**A startup office in your terminal, staffed by real agents.** (v2 — Go + Bubble Tea)

Chat with the boss. Watch the floor — employees get up, walk to the manager,
take the task, type it out, drop the mail, hit the tea machine. The right panel
is yours: chat, agents, board, mail, activity — switch with `tab`.

Underneath the wallpaper, it's all real: the manager is
**[Oikonomos](https://github.com/theboringhumane/oikonomos)**, the employees
are **opencode sub-agents**, the task board is **agentmemory actions**, the
mail room is **agentmemory signals**.

![chat tab](docs/shots-go/chat.png)

```bash
go install github.com/theboringhumane/grafeio/cmd/grafeio@latest

grafeio            # live: spawns `opencode serve`, real boss (oikonomos)
grafeio --demo     # touring mode: simulated events, labeled DEMO
grafeio --server http://127.0.0.1:4096   # attach to an existing server
```

![agents tab](docs/shots-go/agents.png)

Working detail stays visible: opencode-style diffs (line numbers, full-row
red/green tints, inline syntax), thinking blocks, tool calls, permission
prompts (`[y/a/n/esc]`), question prompts, and a message queue that never
locks you out while the boss types.

![diffs](docs/shots-go/chat-diff.png)

Everything live-streams: the boss's replies type out character-by-character on
one bubble, and thinking transcripts unfold in real time before auto-collapsing.

![streaming](docs/shots-go/chat-stream.png)

And the queue is not a tunnel — it's a **backlog the office manages**: items get
board rows, flush goes out as one `[BATCH DISPATCH]` the boss decomposes into
parallel sub-agents (`/route` forces it early), and a dead boss respawns a fresh
session and resends the batch.

## Configure the brain

One file runs the office: **`~/.grafeio/configs/brain.json`** (created with
defaults on first run; inspect anytime with `grafeio --print-default-config`).

```jsonc
{
  "boss":    { "name": "boss (oikonomos)", "model": "anthropic/claude-sonnet-4-5" },
  "roles":   { "developer": { "namePrefix": "tekton" }, "scout": { "namePrefix": "skopos" },
               "reviewer": { "namePrefix": "dikastes" }, "runner": { "namePrefix": "hemerodromos" } },
  "ui":      { "theme": "noir", "power": "auto", "tickMs": 0, "ambientChatter": true },
  "backend": { "agentmemoryUrl": "http://localhost:3111", "server": "", "agentmemoryPollS": 5 }
}
```

Boss model rides every prompt as `{"model":{"providerID","modelID"}}` (serve
1.18.19 `/doc`-verified). Role models are noted as best-effort (sub-agent model
dispatch is opencode's call). `/power`, `/model`, `/theme` all write back.

### Battery (`ui.power`)

| mode | busy | idle | drift (1 min quiet) |
|---|---|---|---|
| `auto` (default) | 180ms ticks | 1s | 3s |
| `performance` | 150ms flat | — | — |
| `saver` | 400ms | 2s | — |

Idle-detection covers streaming, pending replies, walkers, open modals, ambient
bubbles. Renders are memoized (frame digest on the app, `(size, planGen, tick,
renderRev)` on the floor), the agentmemory board poll backs off 2× after five
quiet syncs (cap 4×, reset on change). The office goes cheap when nothing moves.

## Keys

| key | does |
|---|---|
| `tab` / `shift+tab` / `1..5` | switch panel: chat · agents · board · mail · activity |
| `↑` `↓` `pgup` `pgdn` / wheel | scroll the active panel |
| `enter` | send to the boss (chat) |
| `shift+enter` | newline |
| `ctrl+t` | expand/collapse completed thinking blocks |
| `ctrl+d` | expand/collapse diff blocks |
| `y` `a` `n` `esc` | answer a permission prompt |
| `q` / `ctrl+c` | quit |


| key | does |
|---|---|
| `tab` / `shift+tab` / `1..5` | switch panel: chat · agents · board · mail · activity |
| `↑` `↓` `pgup` `pgdn` / wheel | scroll the active panel |
| `enter` | send to the boss (chat) |
| `shift+enter` | newline |
| `q` / `ctrl+c` | quit |

## What v2 (Go) changed

- Chat moved from a fixed bottom bar into a **tabbed right panel** with
  **glamour-rendered markdown** — the boss's `**bold**`, lists and code fences
  format and wrap inside the panel instead of bleeding through the UI.
- Scrolling everywhere (viewport), mouse wheel, multi-line input, typing
  spinner while the boss works.
- New **activity** tab: rolling event log (dispatches, returns, blocks).
- Native single binary. Themes: `--theme noir|paper|mono|dracula|solarized`
  (also `/theme` in-app, persisted to `~/.config/grafeio/theme`).
- Slash commands in chat: `/help /themes /theme <n> /thinking on|off
  /tools on|off /status /clear /quit`.
- The Ink/Node v0.1 app lived on as git tag `node-v0.1.0`.

## Behind the glass

- The boss is a real `opencode serve` primary session — oikonomos manages.
- Employees are real `task` sub-agent sessions (SSE → hire/dispatch/working/returned).
- `docs/shots-go/*` are produced by the app itself + [freeze](https://github.com/charmbracelet/freeze)
  (deterministic scripted backends: `go run ./cmd/uishot`, `go run ./cmd/floorshot`).

Architecture and the floor plan: [docs/architecture.md](docs/architecture.md).

MIT © Lynxlabs
