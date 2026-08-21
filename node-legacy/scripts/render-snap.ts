/**
 * render-snap.ts — floor-only snapshot with a scripted, deterministic stub.
 * Not typechecked (scripts/ is outside tsconfig include); proof-only harness.
 *
 *   npx tsx scripts/render-snap.ts
 *
 * Timeline (stub tick = 50ms):
 *   t0  hires: boss + hr + 4 devs + 1 scout + 1 reviewer
 *   t2  2 dispatches                 t4  working
 *   t6  1 bubble ("big day. lots of meetings.") + 1 blocked (dikastes)
 *   t8  returned + mail              t10 idle-drift coffee
 *
 * Then prints lastFrame() at three sizes:
 *   ===== PLAN A =====  120x26  full zones tour
 *   ===== PLAN B =====   84x22  degraded (cabins collapsed)
 *   ===== PLAN C =====  140x30  busy frame (walkers mid-path, bubble visible)
 *
 * Per frame, TWO verifications:
 *   PLAIN MATCH — strip ANSI, compare line-by-line against buildGridPlain()
 *                 on the same state (proves colored segments recompose to the
 *                 exact same picture; zero layout drift)
 *   COLOR OK    — the raw frame carries escape codes proving:
 *                 yellow boss glyph, cyan bold lit dev screen, red bold blocked
 *                 sprite, magenta cabin-1 glass wall, green plant, white bubble
 *                 text. (PLAN B has no cabins; magenta skipped there.)
 *
 * ink-testing-library hardcodes 100 columns, so we render through ink with a
 * fake 220-col stdout to keep the wide frames intact.
 */
import "./set-color-env.js"; // FIRST: make chalk emit ANSI in this piped harness
import React from "react";
import {EventEmitter} from "node:events";
import {render as inkRender} from "ink";
import {Floor, buildGridPlain} from "../src/office/floor.js";
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
    rawFrame() {
      return stdout.lastFrame() ?? "(no frame)";
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
  6: [
    {type: "bubble", employeeId: "tekton-2", text: "big day. lots of meetings.", ttl: 40},
    {type: "blocked", employeeId: "dikastes", note: "perm ask"}, // red bold at-mailbox, stays blocked
  ],
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

// ------- color verification helpers -------

const PROOF_BUBBLE = "snap check. colors live.";

interface Run {
  codes: Set<string>;
  text: string;
}

/** Parse a raw ink frame into {active codes -> text} runs (chalk open/close codes). */
function parseRuns(raw: string): Run[] {
  const runs: Run[] = [];
  const re = /\x1b\[([0-9;]*)m/g;
  let m: RegExpExecArray | null;
  let codes = new Set<string>();
  let last = 0;
  while ((m = re.exec(raw))) {
    if (m.index > last) {
      runs.push({codes, text: raw.slice(last, m.index)});
      codes = new Set();
    }
    for (const c of m[1].split(";")) if (c) codes.add(c);
    last = re.lastIndex;
  }
  if (last < raw.length) runs.push({codes, text: raw.slice(last)});
  return runs;
}

function hasRun(runs: Run[], codes: string[], match: RegExp): boolean {
  return runs.some((r) => codes.every((c) => r.codes.has(c)) && match.test(r.text));
}

const escapeRegExp = (s: string) => s.replace(/[.*+?^${}()|[\]\\]/g, "\\$&");

/**
 * Verify one captured frame. Returns true when all checks pass.
 *   (a) stripped grid lines === buildGridPlain(snap, w, h-1)  (PLAN layout)
 *   (b) raw grid lines carry the required escape codes        (COLOR)
 */
function verify(
  label: string,
  w: number,
  h: number,
  raw: string,
  snap: OfficeState,
  opts: {cabins: boolean; bubbleText: string},
): boolean {
  let ok = true;

  // (a) PLAN: strip ANSI, compare to the plain renderer line-by-line
  const strippedLines = stripAnsi(raw).replace(/\r/g, "").split("\n").slice(0, h - 1);
  const want = buildGridPlain(snap, w, Math.max(1, h - 1));
  let plainFail = false;
  for (let i = 0; i < Math.max(strippedLines.length, want.length); i++) {
    if (strippedLines[i] !== want[i]) {
      plainFail = true;
      console.error(`PLAIN MISMATCH ${label} row ${i}:`);
      console.error(`  got  |${strippedLines[i] ?? "(missing)"}|`);
      console.error(`  want |${want[i] ?? "(missing)"}|`);
    }
  }
  if (plainFail) ok = false;
  else console.log(`PLAIN MATCH: ${label} (stripped frame === buildGridPlain, ${want.length} rows)`);

  // (b) COLOR: escape codes inside the grid rows (legend excluded)
  const gridRaw = raw.split("\n").slice(0, h - 1).join("\n");
  const runs = parseRuns(gridRaw);
  const bubbleProbe = opts.bubbleText.slice(0, 8);
  const checks: [string, boolean][] = [
    ["yellow", hasRun(runs, ["33"], /M/)], // boss glyph
    ["cyan", hasRun(runs, ["36", "1"], /\[=\]/)], // lit dev screen (cyan bold)
    ["red", hasRun(runs, ["31", "1"], /[A-Z]/)], // blocked sprite (red bold)
    ["green", hasRun(runs, ["32"], /\(Y\)/)], // plant
    ["white", hasRun(runs, ["37"], new RegExp(escapeRegExp(bubbleProbe)))], // bubble text
  ];
  if (opts.cabins) checks.splice(3, 0, ["magenta", hasRun(runs, ["35"], /:/)]); // cabin-1 glass ":"
  const missing = checks.filter(([, pass]) => !pass).map(([name]) => name);
  if (missing.length) {
    ok = false;
    console.error(`COLOR FAIL: ${label} missing ${missing.join(",")}`);
  } else {
    console.log(`COLOR OK: ${checks.map(([name]) => name).join(",")} present`);
  }
  return ok;
}

async function main() {
  // --- run the script at size C so frame C catches walkers mid-path + bubble ---
  // NOTE: verify each frame IMMEDIATELY at capture — every later tick moves
  // walkers (sprites live in a module map), so a frame verified later can no
  // longer match its state snapshot.
  const C = {w: 140, h: 30};
  const viewC = mount(C.w, C.h);
  let frameCraw = "";
  let okC = false;
  for (let t = 0; t <= 10; t++) {
    for (const e of script[t] ?? []) ev(e);
    ev({type: "tick"});
    viewC.rerender();
    await sleep(50); // fast stub ticks
    if (t === 7) {
      frameCraw = viewC.rawFrame(); // tekton-2 mid-walk, bubble fresh, dikastes blocked
      console.log("===== PLAN C =====  140x30  busy frame (walkers mid-path, bubble visible)");
      console.log(frameCraw);
      okC = verify("PLAN C", C.w, C.h, frameCraw, state, {
        cabins: true,
        bubbleText: "big day. lots of meetings.",
      });
    }
  }
  viewC.unmount();

  // --- settle, print and verify the tour frames at the two other sizes ---
  const shoot = async (w: number, h: number, settleTicks: number, label: string, cabins: boolean) => {
    const view = mount(w, h);
    for (let i = 0; i < settleTicks; i++) {
      ev({type: "tick"});
      view.rerender();
    }
    // fresh balloon so the white bubble-text check has a live target.
    // Anchored on hr (cabin/freedesk) so it can never shade the boss office:
    // a balloon over tekton-2 (meeting at the boss desk) hides the yellow boss
    // glyph + nameplate and would break the yellow check by occlusion.
    ev({type: "bubble", employeeId: "hr", text: PROOF_BUBBLE, ttl: 1000});
    ev({type: "tick"});
    view.rerender();
    await sleep(30);
    const raw = view.rawFrame();
    console.log(`===== ${label} =====  ${w}x${h}${cabins ? "  full zones tour" : "  degraded (cabins collapsed)"}`);
    console.log(raw);
    const ok = verify(label, w, h, raw, state, {cabins, bubbleText: PROOF_BUBBLE});
    view.unmount();
    return {raw, ok};
  };

  const A = await shoot(120, 26, 45, "PLAN A", true);
  const B = await shoot(84, 22, 45, "PLAN B", false);

  // --- cabin size assert: glass cabins must survive at the real shell floor
  // size (84x24 grid). Checks: ":"/";"/"." cabin walls present, "[typing]"
  // nameplate absent (no pending chat here), and at least one empty chair
  // rendering dim (gray+dim) instead of staffed-green.
  let okD = true;
  {
    state = {...state, bubbles: []}; // clean frame: no balloons over the cabins
    const view = mount(84, 25); // Floor height 25 -> grid 84x24
    for (let i = 0; i < 45; i++) {
      ev({type: "tick"});
      view.rerender();
    }
    await sleep(30);
    const raw = view.rawFrame();
    const plain = stripAnsi(raw);
    console.log("===== PLAN D =====  84x24  cabin size proof (real shell floor size)");
    console.log(raw);
    const haveWalls = /:{4,}/.test(plain) && /;{4,}/.test(plain) && /\.{4,}/.test(plain);
    if (!haveWalls) {
      okD = false;
      console.error("CABIN FAIL: expected all three glass walls (: ; .) at 84x24");
    }
    if (/\[typing\]/.test(plain)) {
      okD = false;
      console.error("CABIN FAIL: [typing] nameplate with no pending chat");
    }
    const gridRuns = parseRuns(raw.split("\n").slice(0, 24).join("\n"));
    if (!hasRun(gridRuns, ["2"], /\(_\)/)) {
      okD = false;
      console.error("CABIN FAIL: no dim empty chair (empty seats must read unstaffed)");
    }
    view.unmount();
    if (!okD) {
      console.error("SNAP FAIL: cabin size check failed");
      process.exit(1);
    }
    console.log("CABIN SIZE OK");
  }

  // --- BLINK check: catch the idling manager on a blink frame (tick%16===0).
  // Sleep-z's must FLOAT one row above the sprite's right shoulder and never
  // glue into the sprite row: no "zMz" / "zHz" / "zSz" anywhere in the frame.
  // Positive control: at 120 cols the boss anchor is (11,3), so the frame
  // must show the floating "z" at (13,2) — otherwise the check is vacuous.
  let okE = true;
  {
    const view = mount(120, 26);
    let raw = "";
    let blinkTick = -1;
    for (let i = 0; i < 48; i++) {
      ev({type: "tick"});
      view.rerender();
      const boss = state.employees.find((e) => e.id === "manager");
      if (boss?.sprite === "at-desk" && state.tick % 16 === 0) {
        await sleep(30);
        raw = view.rawFrame();
        blinkTick = state.tick;
        break;
      }
      await sleep(5);
    }
    const plain = stripAnsi(raw);
    console.log(`===== PLAN E =====  120x26  blink frame (tick ${blinkTick}, z floats above the shoulder)`);
    console.log(raw);
    if (blinkTick < 0) {
      okE = false;
      console.error("BLINK FAIL: never caught the manager at-desk on a blink frame");
    }
    const glued = plain.split("\n").filter((l) => /zMz|zHz|zSz/.test(l)); // machine-format frame grep, not NL
    if (glued.length) {
      okE = false;
      console.error(`BLINK FAIL: z glued into a sprite row: ${glued.map((l) => JSON.stringify(l)).join(", ")}`);
    }
    const row2 = plain.replace(/\r/g, "").split("\n")[2] ?? "";
    if (row2[13] !== "z") {
      okE = false;
      console.error(`BLINK FAIL: no floating z at (13,2) above the manager; row 2 = |${row2}|`);
    }
    view.unmount();
    if (!okE) {
      console.error("SNAP FAIL: blink check failed");
      process.exit(1);
    }
    console.log("BLINK OK: no zMz/zHz/zSz in any row; floating z present at (13,2)");
  }

  // layout-stability proof: PLAN A with escapes made literal ([33m style), then plain
  console.log("===== PROOF: PLAN A raw (escapes literal, \\x1b[ shown as [) =====");
  console.log(A.raw.replace(/\x1b\[/g, "["));
  console.log("===== PROOF: PLAN A plain (ANSI stripped) =====");
  console.log(stripAnsi(A.raw));

  if (!A.ok || !B.ok || !okC) {
    console.error("SNAP FAIL: one or more frames failed verification");
    process.exit(1);
  }
  console.log("SNAP OK: all frames verified");
  process.exit(0);
}

await main();
