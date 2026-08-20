/**
 * render-shell.ts — snapshot the opencode-style chrome (topbar / floor+sidebar /
 * chat bar / statusbar) with a scripted stub backend, at a fixed 120x36 geometry.
 * Own harness (render-snap.ts is owned by the floor dev): custom fake stdout,
 * because ink-testing-library hardcodes 100 columns. Not typechecked.
 *
 *   npx tsx scripts/render-shell.ts
 *
 * Prints lastFrame() twice between markers:
 *   ===== SHELL A =====  ~1.5s (staffed, calm, awaiting work)
 *   ===== SHELL B =====  ~4.0s (busy: blocked red, mail, board moved, bubbles)
 */
import React from "react";
import {EventEmitter} from "node:events";
import {render as inkRender} from "ink";
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

const sleep = (ms: number) => new Promise((r) => setTimeout(r, ms));
const stripAnsi = (s: string) => s.replace(/\x1b\[[0-9;]*[A-Za-z]/g, "");

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

  await sleep(1500);
  console.log("===== SHELL A (~1.5s, staffing calm) =====");
  console.log(stripAnsi(stdout.lastFrame() ?? "(no frame)"));

  await sleep(2500);
  console.log("===== SHELL B (~4.0s, busy) =====");
  console.log(stripAnsi(stdout.lastFrame() ?? "(no frame)"));

  inst.unmount();
  process.exit(0);
}

await main();
