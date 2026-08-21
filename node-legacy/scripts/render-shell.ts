/**
 * render-shell.ts — snapshot the opencode-style chrome (topbar / floor+sidebar /
 * chat bar / statusbar) with a scripted stub backend, at a fixed 120x36 geometry.
 * Own harness (render-snap.ts is owned by the floor dev): custom fake stdout,
 * because ink-testing-library hardcodes 100 columns. Not typechecked.
 *
 *   npx tsx scripts/render-shell.ts           (stripped frames, as before)
 *   npx tsx scripts/render-shell.ts --escape  (ANSI escapes literal, [33m form)
 *
 * Prints lastFrame() twice between markers:
 *   ===== SHELL A =====  ~1.5s (staffed, calm, awaiting work)
 *   ===== SHELL B =====  ~4.0s (busy: blocked red, mail, board moved, bubbles)
 *
 * After each frame it asserts:
 *   - ANSI color codes present for boss yellow / dev cyan / done green / blocked red
 *     -> prints "SHELL COLOR OK"
 *   - stripped geometry unchanged vs the pre-color monochrome layout
 *     (ROWS lines; sidebar titles AGENTS/MAIL/BOARD at column 86)
 *     -> prints "SHELL GEOMETRY OK"
 * Any assertion failure exits non-zero.
 */
import React from "react";
import {EventEmitter} from "node:events";
import {render as inkRender} from "ink";
import chalk from "chalk";
import {App} from "../src/app.js";
import type {
  BoardTask,
  Employee,
  MailItem,
  OfficeBackend,
  OfficeEvent,
} from "../src/state.js";

const COLS = 120;
const ROWS = 36;

/** Sidebar chrome starts at COLS - 36 (SIDEBAR_W in app.tsx); titles sit 1 border + 1 padding in. */
const SIDEBAR_TITLE_COL = COLS - 36 + 2; // 86

const ESCAPE_MODE = process.argv.includes("--escape");

// The harness pipes stdout, so chalk autodetects level 0 and ink renders plain.
// Colors are the whole point of this snapshot: force them on.
if (chalk.level === 0) chalk.level = 1;

const sleep = (ms: number) => new Promise((r) => setTimeout(r, ms));
const stripAnsi = (s: string) => s.replace(/\x1b\[[0-9;]*[A-Za-z]/g, "");
/** Show CSI sequences literally: ESC[33m -> "[33m" (keeps the frame readable). */
const escapeAnsi = (s: string) => s.replace(/\x1b\[([0-9;]*)([A-Za-z])/g, "[$1$2");

let failures = 0;

function assertColors(frame: string, label: string) {
  const checks: Array<[string, RegExp]> = [
    ["boss yellow", /\x1b\[33m/],
    ["dev cyan", /\x1b\[36m/],
    ["done green", /\x1b\[32m/],
    ["blocked red", /\x1b\[31m/],
  ];
  const missing = checks.filter(([, re]) => !re.test(frame)).map(([name]) => name);
  if (missing.length > 0) {
    failures++;
    console.log(`${label} SHELL COLOR FAIL — missing: ${missing.join(", ")}`);
  } else {
    console.log(`${label} SHELL COLOR OK (boss yellow, dev cyan, done green, blocked red)`);
  }
}

function assertGeometry(stripped: string, label: string) {
  const problems: string[] = [];
  const lines = stripped.replace(/\n+$/, "").split("\n");
  if (lines.length !== ROWS) problems.push(`line count ${lines.length} != ${ROWS}`);
  for (const title of ["AGENTS", "MAIL", "BOARD"]) {
    const row = lines.find((l) => l.includes(` ${title}`));
    if (!row) {
      problems.push(`sidebar title ${title} missing`);
      continue;
    }
    const col = row.indexOf(title);
    if (col !== SIDEBAR_TITLE_COL)
      problems.push(`sidebar title ${title} at col ${col} != ${SIDEBAR_TITLE_COL}`);
  }
  if (problems.length > 0) {
    failures++;
    console.log(`${label} SHELL GEOMETRY FAIL — ${problems.join("; ")}`);
  } else {
    console.log(`${label} SHELL GEOMETRY OK (${ROWS} lines; AGENTS/MAIL/BOARD at col ${SIDEBAR_TITLE_COL})`);
  }
}

/** Minimal ink-compatible stdout pinned to 120x36 (ink-testing-library is 100-wide). */
class FakeStdout extends EventEmitter {
  get columns() {
    return COLS;
  }
  get rows() {
    return ROWS;
  }
  frames: string[] = [];
  private last = "";
  write = (frame: string) => {
    this.frames.push(frame);
    this.last = frame;
  };
  lastFrame = () => this.last;
}

class FakeStderr extends EventEmitter {
  frames: string[] = [];
  write = (frame: string) => {
    this.frames.push(frame);
  };
}

class FakeStdin extends EventEmitter {
  isTTY = true;
  private data: string | null = null;
  write = (d: string) => {
    this.data = d;
    this.emit("readable");
    this.emit("data", d);
  };
  setEncoding() {}
  setRawMode() {}
  resume() {}
  pause() {}
  ref() {}
  unref() {}
  read = () => {
    const d = this.data;
    this.data = null;
    return d;
  };
}

function stubBackend(): OfficeBackend {
  let emitFn: ((e: OfficeEvent) => void) | null = null;
  let timer: ReturnType<typeof setInterval> | null = null;
  let t = 0;

  const emp = (id: string, name: string, role: Employee["role"]): Employee => ({
    id,
    name,
    role,
    seat: "",
    sprite: "at-desk",
  });
  const task = (id: string, title: string): BoardTask => ({
    id,
    title,
    status: "pending",
    at: Date.now(),
  });
  const mail = (id: string, from: string, to: string, subject: string): MailItem => ({
    id,
    from,
    to,
    subject,
    at: Date.now(),
    body: subject,
    kind: "return",
  });
  const chat = (id: string, from: "user" | "boss", text: string, pending = false) =>
    from === "user"
      ? ({type: "chat-user", msg: {id, from, text, at: Date.now()}} as OfficeEvent)
      : ({type: "chat-boss", msg: {id, from, text, at: Date.now(), pending}} as OfficeEvent);

  // stub tick = 50ms; 20 stub-ticks = 1s. App ticks run at 180ms real time.
  const script: Record<number, OfficeEvent[]> = {
    0: [{type: "status", text: "[grafeio] demo - staffing the floor..."}],
    2: [
      {type: "hire", employee: emp("hr", "hr", "hr")},
      {type: "hire", employee: emp("tekton-1", "tekton-1", "developer")},
    ],
    6: [
      {type: "hire", employee: emp("tekton-2", "tekton-2", "developer")},
      {type: "hire", employee: emp("tekton-3", "tekton-3", "developer")},
    ],
    10: [
      {type: "hire", employee: emp("tekton-4", "tekton-4", "developer")},
      {type: "hire", employee: emp("skopos-1", "skopos-1", "scout")},
    ],
    16: [{type: "status", text: "[grafeio] demo - awaiting work"}],
    // ---- SHELL A taken here (~1.5s): staffed, calm ----
    32: [
      {type: "dispatch", task: task("t-1", "boot ink shell"), employeeId: "tekton-1"},
      {type: "dispatch", task: task("t-2", "wire SSE feed"), employeeId: "tekton-2"},
    ],
    36: [{type: "working", employeeId: "tekton-1", taskId: "t-1"}],
    40: [{type: "blocked", employeeId: "tekton-3", note: "tekton-3 needs permission: npm publish"}],
    44: [{type: "working", employeeId: "tekton-2", taskId: "t-2"}],
    50: [
      {
        type: "returned",
        employeeId: "tekton-2",
        taskId: "t-2",
        mail: mail("m-1", "tekton-2", "boss", "SSE feed wired"),
      },
    ],
    54: [
      chat("c-1", "user", "status check - how is the SSE feed?"),
      chat("c-2", "boss", "", true),
    ],
    60: [chat("c-3", "boss", "SSE wired. tekton-1 is on the shell next.")],
    64: [chat("c-4", "user", "ship the shell rewrite next"), chat("c-5", "boss", "", true)],
    70: [chat("c-6", "boss", "queued. dispatching as soon as tekton-1 frees up.")],
    74: [{type: "bubble", employeeId: "tekton-1", text: "this diff is a crime scene."}],
    78: [{type: "bubble", employeeId: "tekton-3", text: "who took the red mug?"}],
    // ---- SHELL B taken here (~4.0s): busy ----
  };

  return {
    mode: "demo",
    async start(emit) {
      emitFn = emit;
      timer = setInterval(() => {
        const evs = script[t];
        if (evs) for (const e of evs) emit(e);
        t++;
      }, 50);
    },
    async send(text) {
      emitFn?.({
        type: "chat-user",
        msg: {id: `u-${Date.now()}`, from: "user", text, at: Date.now()},
      });
    },
    async stop() {
      if (timer) clearInterval(timer);
    },
  };
}

async function main() {
  const stdout = new FakeStdout();
  const stderr = new FakeStderr();
  const stdin = new FakeStdin();
  const inst = inkRender(
    React.createElement(App, {backend: stubBackend(), cols: COLS, rows: ROWS}),
    {
      stdout: stdout as never,
      stderr: stderr as never,
      stdin: stdin as never,
      debug: true,
      exitOnCtrlC: false,
      patchConsole: false,
    },
  );

  const frames: Array<{label: string; marker: string; frame: string}> = [];

  await sleep(1500);
  frames.push({
    label: "A",
    marker: "===== SHELL A (~1.5s, staffing calm) =====",
    frame: stdout.lastFrame() ?? "(no frame)",
  });

  await sleep(2500);
  frames.push({
    label: "B",
    marker: "===== SHELL B (~4.0s, busy) =====",
    frame: stdout.lastFrame() ?? "(no frame)",
  });

  inst.unmount();

  for (const {label, marker, frame} of frames) {
    console.log(marker);
    console.log(ESCAPE_MODE ? escapeAnsi(frame) : stripAnsi(frame));
    assertColors(frame, `SHELL ${label}`);
    assertGeometry(stripAnsi(frame), `SHELL ${label}`);
  }

  process.exit(failures === 0 ? 0 : 1);
}

await main();
