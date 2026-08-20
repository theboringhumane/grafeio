/**
 * render-snap.ts — floor-only snapshot with a scripted, deterministic stub.
 * Not typechecked (scripts/ is outside tsconfig include); proof-only harness.
 *
 *   npx tsx scripts/render-snap.ts
 *
 * Timeline (stub tick = 50ms):
 *   t0  hires: boss + hr + 4 devs + 1 scout + 1 reviewer
 *   t2  2 dispatches                 t4  working
 *   t6  1 bubble ("big day. lots of meetings.")
 *   t8  returned + mail              t10 idle-drift coffee
 *
 * Then prints lastFrame() at three sizes:
 *   ===== PLAN A =====  120x26  full zones tour
 *   ===== PLAN B =====   84x22  degraded (cabins collapsed)
 *   ===== PLAN C =====  140x30  busy frame (walkers mid-path, bubble visible)
 *
 * ink-testing-library hardcodes 100 columns, so we render through ink with a
 * fake 220-col stdout to keep the wide frames intact.
 */
import React from "react";
import {EventEmitter} from "node:events";
import {render as inkRender} from "ink";
import {Floor} from "../src/office/floor.js";
import {initialState, officeReducer} from "../src/app.js";
import type {
  BoardTask,
  Employee,
  MailItem,
  OfficeEvent,
  OfficeState,
} from "../src/state.js";

const sleep = (ms: number) => new Promise((r) => setTimeout(r, ms));
const stripAnsi = (s: string) => s.replace(/\x1b\[[0-9;]*[A-Za-z]/g, "");

class FakeStdout extends EventEmitter {
  frames: string[] = [];
  private last?: string;
  get columns() {
    return 220;
  }
  get rows() {
    return 80;
  }
  write = (frame: string) => {
    this.frames.push(frame);
    this.last = frame;
  };
  lastFrame() {
    return this.last;
  }
}

class FakeStdin extends EventEmitter {
  isTTY = true;
  data: string | null = null;
  setEncoding() {}
  setRawMode() {}
  resume() {}
  pause() {}
  ref() {}
  unref() {}
  read() {
    const d = this.data;
    this.data = null;
    return d;
  }
}

function mount(width: number, height: number) {
  const stdout = new FakeStdout();
  const inst = inkRender(React.createElement(Floor, {state, width, height}) as any, {
    stdout: stdout as any,
    stderr: new FakeStdout() as any,
    stdin: new FakeStdin() as any,
    debug: true,
    exitOnCtrlC: false,
    patchConsole: false,
  });
  return {
    rerender() {
      inst.rerender(React.createElement(Floor, {state, width, height}));
    },
    frame() {
      return stripAnsi(stdout.lastFrame() ?? "(no frame)");
    },
    unmount() {
      inst.unmount();
    },
  };
}

// ------- scripted stub backend state -------
let state: OfficeState = {...initialState("demo"), bubbles: []};

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

const script: Record<number, OfficeEvent[]> = {
  0: [
    {type: "hire", employee: emp("manager", "boss", "manager")}, // dedup: already seated
    {type: "hire", employee: emp("hr", "hr", "hr")}, // dedup: already seated
    {type: "hire", employee: emp("tekton-1", "tekton-1", "developer")},
    {type: "hire", employee: emp("tekton-2", "tekton-2", "developer")},
    {type: "hire", employee: emp("tekton-3", "tekton-3", "developer")},
    {type: "hire", employee: emp("tekton-4", "tekton-4", "developer")},
    {type: "hire", employee: emp("skopos-1", "skopos-1", "scout")},
    {type: "hire", employee: emp("dikastes", "dikastes", "reviewer")},
    {type: "status", text: "SNAP: floor script online (t0)"},
  ],
  2: [
    {type: "dispatch", task: task("t-1", "boot ink shell"), employeeId: "tekton-1"},
    {type: "dispatch", task: task("t-2", "wire SSE feed"), employeeId: "tekton-2"},
  ],
  4: [
    {type: "working", employeeId: "tekton-1", taskId: "t-1"},
    {type: "working", employeeId: "tekton-3"},
  ],
  6: [{type: "bubble", employeeId: "tekton-2", text: "big day. lots of meetings.", ttl: 40}],
  8: [
    {
      type: "returned",
      employeeId: "tekton-1",
      taskId: "t-1",
      mail: mail("m-1", "tekton-1", "boss", "ink shell booted"),
    },
    {type: "mail", mail: mail("m-2", "dikastes", "boss", "review queue clean")},
  ],
  10: [{type: "idle-drift", employeeId: "skopos-1"}],
};

/** Apply an event; "bubble" is handled here (reducer lives in the shell's app.tsx). */
function ev(e: OfficeEvent): void {
  if (e.type === "bubble") {
    const b = {
      id: `bub-${state.tick}`,
      employeeId: e.employeeId,
      text: e.text,
      untilTick: state.tick + (e.ttl ?? 40),
    };
    state = {
      ...state,
      bubbles: [...(state.bubbles ?? []).filter((x) => x.untilTick > state.tick), b],
    };
    return;
  }
  state = officeReducer(state, e);
}

async function main() {
  // --- run the script at size C so frame C catches walkers mid-path + bubble ---
  const C = {w: 140, h: 30};
  const viewC = mount(C.w, C.h);
  let frameC = "";
  for (let t = 0; t <= 10; t++) {
    for (const e of script[t] ?? []) ev(e);
    ev({type: "tick"});
    viewC.rerender();
    await sleep(50); // fast stub ticks
    if (t === 7) frameC = viewC.frame(); // tekton-2 mid-walk, bubble fresh
  }
  viewC.unmount();

  // --- settle + shoot the tour frames at the two other sizes ---
  const shoot = async (w: number, h: number, settleTicks: number) => {
    const view = mount(w, h);
    for (let i = 0; i < settleTicks; i++) {
      ev({type: "tick"});
      view.rerender();
    }
    await sleep(30);
    const out = view.frame();
    view.unmount();
    return out;
  };

  const frameA = await shoot(120, 26, 45);
  const frameB = await shoot(84, 22, 45);

  console.log("===== PLAN A =====  120x26  full zones tour");
  console.log(frameA);
  console.log("===== PLAN B =====  84x22   degraded (cabins collapsed)");
  console.log(frameB);
  console.log("===== PLAN C =====  140x30  busy frame (walkers mid-path, bubble visible)");
  console.log(frameC);
  process.exit(0);
}

await main();
