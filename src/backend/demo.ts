/**
 * demo.ts — the scripted touring backend (`grafeio --demo`). No network,
 * no SDK: one believable day at the office played back on a timer chain.
 *
 * Every sprite move, board row and mail item is expressed ONLY through
 * OfficeEvents (hire/dispatch/working/returned/blocked/idle-drift/task/
 * mail/chat-*), so the UI animates exactly the way it does in live mode.
 *
 * Tick ownership: this backend emits a "tick" event every 180ms so --demo
 * works even before index.ts wires an animation timer. The LIVE backend
 * (opencode.ts) deliberately does NOT emit ticks — index.ts drives those.
 */
import type {
  BoardTask,
  Employee,
  MailItem,
  OfficeBackend,
  OfficeEvent,
} from "../state.js";
import { shortTitle } from "./events.js";

const TICK_MS = 180;
const PULSE_MS = 700;
const AMBIENT_MS = 8_000;

function employee(id: string, role: Employee["role"], seat: string): Employee {
  return { id, name: id, role, seat, sprite: "at-desk" };
}

function task(id: string, title: string, owner: string): BoardTask {
  return { id, title, status: "in-progress", owner, at: Date.now() };
}

export function createDemoBackend(): OfficeBackend {
  return new DemoBackend();
}

class DemoBackend implements OfficeBackend {
  readonly mode = "demo" as const;

  private emitRef?: (e: OfficeEvent) => void;
  private stopped = false;
  private timers = new Set<ReturnType<typeof setTimeout>>();
  private intervals = new Set<ReturnType<typeof setInterval>>();

  private roster: Employee[] = [];
  private taskById = new Map<string, BoardTask>();
  /** employees currently on a brief (receives working pulses) */
  private active: string[] = [];
  /** employees waving at the mailbox (no pulses until they return) */
  private blockedIds = new Set<string>();
  private pulseIdx = 0;
  private ambientBeat = 0;
  private adHocSeq = 0;
  private chatSeq = 0;

  async start(emit: (e: OfficeEvent) => void): Promise<void> {
    this.emitRef = emit;

    // t0: floor opens. Manager + hr are permanent seats.
    emit({ type: "status", text: "DEMO - simulated events (no real agents)" });
    this.hire(employee("manager", "manager", "manager"));
    this.hire(employee("hr", "hr", "hr"));

    // t+400ms: the boss hands out the first two briefs.
    this.at(400, () => {
      this.hire(employee("tekton-1", "developer", "desk-1"));
      this.dispatch("t1", "Wire the SSE stream into the office reducer", "tekton-1");
      this.hire(employee("skopos-1", "scout", "desk-2"));
      this.dispatch("t2", "Map the repo's event flow end to end", "skopos-1");
    });

    // t+1s: a third hire joins the first brief wave.
    this.at(1_000, () => {
      this.hire(employee("tekton-2", "developer", "desk-3"));
      this.dispatch("t3", "Draft the demo smoke script", "tekton-2");
    });

    // Working pulses: typing frames for whoever is on a brief, round-robin.
    // Blocked folks are at the mailbox waving, not typing — skip them.
    this.every(PULSE_MS, () => {
      const free = this.active.filter((id) => !this.blockedIds.has(id));
      if (free.length === 0) return;
      const id = free[this.pulseIdx++ % free.length];
      const t = [...this.taskById.values()].find((x) => x.owner === id && x.status === "in-progress");
      this.emit({ type: "working", employeeId: id, taskId: t?.id });
    });

    // t+2.5s: the scout returns with findings.
    this.at(2_500, () => {
      this.doReturn("skopos-1", "t2", {
        subject: "return: scout report",
        body: "Scout report: events.ts maps 8 SSE types cleanly. Only child-idle and boss-complete need fetches; the rest are pure. No blockers to wiring the reducer.",
      });
    });

    // t+4s: tekton-2 ships the smoke script.
    this.at(4_000, () => {
      this.doReturn("tekton-2", "t3", {
        subject: "return: demo smoke script",
        body:
          "DONE - smoke script records 6.5s of demo events and asserts the floor contract.\n" +
          "FILES - scripts/smoke-demo.ts.\n" +
          "VERIFY - npx tsx scripts/smoke-demo.ts prints SMOKE OK.",
      });
    });

    // t+5.5s: tekton-1 hits a permission gate and waves at the mailbox...
    this.at(5_500, () => {
      this.blockedIds.add("tekton-1");
      this.emit({ type: "blocked", employeeId: "tekton-1", note: "permission: write src/app.tsx" });
    });

    // ...approved; t+6.5s the brief lands in the tray.
    this.at(6_500, () => {
      this.doReturn("tekton-1", "t1", {
        subject: "return: SSE wiring",
        body: "DONE - reducer consumes hire/dispatch/working/returned/blocked and the floor animates. VERIFY: demo timeline replays the whole flow without SDK calls.",
      });
    });

    // t+7s: someone drifts to the tea machine.
    this.at(7_000, () => {
      this.emit({ type: "idle-drift", employeeId: "skopos-1" });
    });

    // Ambient life: gentle working pulses, occasional coffee, forever.
    this.every(AMBIENT_MS, () => {
      this.ambientBeat++;
      const folks = ["tekton-1", "skopos-1", "tekton-2"];
      const who = folks[this.ambientBeat % folks.length];
      this.emit({ type: "working", employeeId: who });
      if (this.ambientBeat % 3 === 0) {
        this.emit({ type: "idle-drift", employeeId: folks[(this.ambientBeat + 1) % folks.length] });
      }
    });

    // Animation frames. DEMO emits these itself (see module docblock).
    this.every(TICK_MS, () => this.emit({ type: "tick" }));
  }

  async send(text: string): Promise<void> {
    const emit = this.emitRef;
    const trimmed = text.trim();
    if (!emit || !trimmed || this.stopped) return;

    emit({ type: "chat-user", msg: { id: `user-${++this.chatSeq}`, from: "user", text: trimmed, at: Date.now() } });

    // The demo boss always answers cheerily, naming the request.
    this.at(600, () => {
      this.emit({
        type: "chat-boss",
        msg: {
          id: `boss-${this.chatSeq}`,
          from: "boss",
          text: `On it: "${shortTitle(trimmed, 40)}" is on the board - watch the floor.`,
          at: Date.now(),
          pending: false,
        },
      });
    });

    // ...and one ad-hoc dispatch cycle proves the request landed.
    this.at(900, () => {
      const assignee = this.adHocSeq % 2 === 0 ? "tekton-1" : "tekton-2";
      this.dispatch(`adhoc-${++this.adHocSeq}`, `Ad-hoc: ${shortTitle(trimmed, 36)}`, assignee);
    });
  }

  async stop(): Promise<void> {
    if (this.stopped) return;
    this.stopped = true;
    for (const t of this.timers) clearTimeout(t);
    this.timers.clear();
    for (const i of this.intervals) clearInterval(i);
    this.intervals.clear();
  }

  // ---------------------------------------------------------------- script

  private emit(e: OfficeEvent): void {
    if (!this.stopped) this.emitRef?.(e);
  }

  /** setTimeout, tracked for stop(). */
  private at(ms: number, fn: () => void): void {
    const t = setTimeout(() => {
      this.timers.delete(t);
      if (!this.stopped) fn();
    }, ms);
    this.timers.add(t);
  }

  /** setInterval, tracked for stop(). */
  private every(ms: number, fn: () => void): void {
    const i = setInterval(() => {
      if (!this.stopped) fn();
    }, ms);
    this.intervals.add(i);
  }

  private hire(e: Employee): void {
    this.roster.push(e);
    this.emit({ type: "hire", employee: { ...e } });
  }

  private dispatch(taskId: string, title: string, owner: string): void {
    const t = task(taskId, title, owner);
    this.taskById.set(taskId, t);
    if (!this.active.includes(owner)) this.active.push(owner);
    this.emit({ type: "dispatch", task: t, employeeId: owner });
  }

  private doReturn(employeeId: string, taskId: string, mailDraft: { subject: string; body: string }): void {
    const prev = this.taskById.get(taskId);
    const done: BoardTask = { ...(prev ?? task(taskId, "untitled brief", employeeId)), status: "done" };
    this.taskById.set(taskId, done);
    this.active = this.active.filter((id) => id !== employeeId);
    this.blockedIds.delete(employeeId);
    const mail: MailItem = {
      id: `mail-${taskId}`,
      from: employeeId,
      to: "manager",
      at: Date.now(),
      subject: mailDraft.subject,
      body: mailDraft.body,
      kind: "return",
    };
    this.emit({ type: "task", task: done });
    this.emit({ type: "returned", employeeId, taskId: done.id, mail });
  }
}
