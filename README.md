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

## Keys

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
