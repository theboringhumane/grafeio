# Grafeio (γραφείο)

**A startup office in your terminal, staffed by real agents.**

Chat with the boss. Watch the floor — employees get up, walk to the manager,
take the task, type it out, drop the mail, hit the tea machine. The board on
the right tracks pending/in-progress/completed. The mail tray on the left
carries every brief and return. HR keeps the roster.

Underneath the wallpaper, it's all real: the manager is **Oikonomos**, the
employees are **opencode sub-agents**, the task board is **agentmemory
actions**, the mail room is **agentmemory signals**.

```bash
npm i -g grafeio
grafeio            # attach to your opencode install (spawns `opencode serve`)
grafeio --demo     # touring mode: simulated events, no install required
```

Layout:

```
+---------------------------------------------------------------+
|  FLOOR              |  MAIL            |  BOARD               |
|  (animated office)  |  (agent mail)    |  pending|doing|done  |
+---------------------+------------------+----------------------+
|  CHAT —> the boss (oikonomos)                                  |
+---------------------------------------------------------------+
```

Requirements: Node 20+, opencode (`brew install anomalyco/tap/opencode`) for
the live mode.

Architecture: [docs/architecture.md](docs/architecture.md) — deliberately
small enough to hold in your head.

MIT © Lynxlabs
