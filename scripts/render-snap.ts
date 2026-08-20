/**
 * render-snap.ts — snapshot the App with a scripted, deterministic stub backend.
 * Not typechecked (scripts/ is outside tsconfig include); proof-only harness.
 *
 *   npx tsx scripts/render-snap.ts
 *
 * Prints lastFrame() at two moments between ===== SNAP ===== markers:
 *   A ~0.4s (idle/staffing scene)  B ~2.0s (mid-action: mail + coffee walker)
 */
import React from "react";
import {render} from "ink-testing-library";
import {App} from "../src/app.js";
import type {
  BoardTask,
  Employee,
  MailItem,
  OfficeBackend,
  OfficeEvent,
} from "../src/state.js";

const sleep = (ms: number) => new Promise((r) => setTimeout(r, ms));
const stripAnsi = (s: string) => s.replace(/\x1b\[[0-9;]*[A-Za-z]/g, "");

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

  // stub tick = 50ms; script fires on stub-tick counts
  const script: Record<number, OfficeEvent[]> = {
    0: [{type: "status", text: "SNAP: demo script online (t0)"}],
    1: [
      {type: "hire", employee: emp("hr", "hr", "hr")},
      {type: "hire", employee: emp("tekton-1", "tekton-1", "developer")},
      {type: "hire", employee: emp("tekton-2", "tekton-2", "developer")},
      {type: "hire", employee: emp("skopos-1", "skopos-1", "scout")},
    ],
    2: [
      {type: "dispatch", task: task("t-1", "boot ink shell"), employeeId: "tekton-1"},
      {type: "dispatch", task: task("t-2", "wire SSE feed"), employeeId: "tekton-2"},
    ],
    3: [{type: "working", employeeId: "tekton-1", taskId: "t-1"}],
    5: [
      {
        type: "returned",
        employeeId: "tekton-2",
        taskId: "t-2",
        mail: mail("m-1", "tekton-2", "boss", "SSE feed wired"),
      },
    ],
    7: [{type: "idle-drift", employeeId: "skopos-1"}],
    9: [
      {
        type: "chat-user",
        msg: {id: "c-1", from: "user", text: "status check - how is the SSE feed?", at: Date.now()},
      },
      {type: "chat-boss", msg: {id: "c-2", from: "boss", text: "", at: Date.now(), pending: true}},
    ],
    12: [
      {
        type: "chat-boss",
        msg: {id: "c-3", from: "boss", text: "SSE wired. tekton-1 is on the shell next.", at: Date.now()},
      },
    ],
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
  const inst = render(React.createElement(App, {backend: stubBackend()}));

  await sleep(400);
  console.log("===== SNAP t=A (0.4s) =====");
  console.log(stripAnsi(inst.lastFrame() ?? "(no frame)"));

  await sleep(1600);
  console.log("===== SNAP t=B (2.0s) =====");
  console.log(stripAnsi(inst.lastFrame() ?? "(no frame)"));

  inst.unmount();
  process.exit(0);
}

await main();
