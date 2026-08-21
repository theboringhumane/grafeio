/**
 * opencode.ts — the LIVE backend for grafeio.
 *
 * Responsibilities:
 *   - resolve an opencode server: opts.baseUrl -> env OPENCODE_SERVER ->
 *     spawn `opencode serve --port 0 --hostname 127.0.0.1` (URL parsed from
 *     stdout, 10s timeout; the spawned child is ours to kill on stop()).
 *   - run an @opencode-ai/sdk client against it and find-or-create the
 *     primary ("boss") session for this directory.
 *   - subscribe to the SSE event stream and normalize via events.ts (pure),
 *     plus the two mapping branches that need I/O and therefore live here:
 *       * child session goes idle  -> pull its messages; if an assistant
 *         text part exists, emit returned + return-mail, mark its task
 *         done, and best-effort delete the child 10s later (-> fire).
 *       * primary assistant message completes -> pull the reply text and
 *         emit chat-boss with the matching pending-bubble id.
 *   - sync board + mail from agentmemory every 5s when the probe found it;
 *     otherwise the board is derived from dispatch/returned flows.
 *
 * Chat path: send() drives the SAME emit callback the app passed to
 * start() (a small internal ref, `this.emitRef`) so the user message and
 * the pending boss bubble hit state immediately; promptAsync returns at
 * once and the SSE stream drives the real completion.
 *
 * Note: unlike demo.ts, this backend never emits "tick" — index.ts owns
 * the animation timer for live mode.
 */
import { spawn, type ChildProcess } from "node:child_process";
import {
  createOpencodeClient,
  type Event,
  type Message,
  type OpencodeClient,
  type Session,
} from "@opencode-ai/sdk";
import type { BoardTask, MailItem, OfficeBackend, OfficeEvent } from "../state.js";
import { createNormCtx, mapOpencodeEvent, shortTitle, type NormCtx } from "./events.js";
import { probeAgentmemory, type AgentmemoryHandle } from "./agentmemory.js";

export interface LiveBackendOpts {
  /** attach to this URL instead of spawning */
  baseUrl?: string;
  /** project directory the primary session belongs to */
  directory: string;
}

/** Human-readable error text out of SDK error shapes or Exceptions. */
function errText(err: unknown): string {
  if (err instanceof Error) return err.message;
  if (err && typeof err === "object") {
    const o = err as Record<string, unknown>;
    for (const candidate of [o.error, o]) {
      if (candidate && typeof candidate === "object") {
        const m = (candidate as Record<string, unknown>).message;
        if (typeof m === "string") return m;
      }
    }
    try {
      return JSON.stringify(err).slice(0, 200);
    } catch {
      /* fall through */
    }
  }
  return String(err);
}

/** Spawn `opencode serve`, resolve with the listening URL from stdout. */
function spawnServe(directory: string): Promise<{ url: string; proc: ChildProcess }> {
  return new Promise((resolve, reject) => {
    const proc = spawn("opencode", ["serve", "--port", "0", "--hostname", "127.0.0.1"], {
      cwd: directory,
      stdio: ["ignore", "pipe", "pipe"],
    });
    let done = false;
    let output = "";
    const timer = setTimeout(() => {
      if (done) return;
      done = true;
      proc.kill("SIGTERM");
      reject(new Error("opencode serve: no listening URL within 10s"));
    }, 10_000);
    const fail = (why: string) => {
      if (done) return;
      done = true;
      clearTimeout(timer);
      reject(new Error(why));
    };
    proc.on("error", (e) => fail(`opencode serve spawn failed: ${e.message}`));
    proc.on("exit", (code) =>
      fail(`opencode serve exited (${code}) before printing a URL: ${output.trim().slice(0, 200)}`),
    );
    proc.stdout.on("data", (chunk: Buffer) => {
      if (done) return;
      output += chunk.toString();
      const m = output.match(/(https?:\/\/\S+)/);
      if (m) {
        done = true;
        clearTimeout(timer);
        resolve({ url: m[1].replace(/[.,;)\]]+$/, ""), proc });
      }
    });
    proc.stderr.on("data", (chunk: Buffer) => {
      output += chunk.toString();
    });
  });
}

export function createLiveBackend(opts: LiveBackendOpts): OfficeBackend {
  return new LiveBackend(opts);
}

class LiveBackend implements OfficeBackend {
  readonly mode = "live" as const;

  private client?: OpencodeClient;
  private proc?: ChildProcess;
  private stream?: AsyncGenerator<Event>;
  private am?: AgentmemoryHandle;
  private emitRef?: (e: OfficeEvent) => void;
  private primaryId = "";
  private ctx: NormCtx = createNormCtx();
  private stopped = false;

  private timers = new Set<ReturnType<typeof setTimeout>>();
  private poll?: ReturnType<typeof setInterval>;

  // chat bookkeeping: FIFO of pending boss bubbles, completed assistant ids
  private chatSeq = 0;
  private pendingBoss: string[] = [];
  private bossCompleted = new Set<string>();
  // agentmemory sync dedupe
  private amTasks = new Map<string, string>();
  private amMails = new Set<string>();

  constructor(private opts: LiveBackendOpts) {}

  async start(emit: (e: OfficeEvent) => void): Promise<void> {
    this.emitRef = emit;

    let url = this.opts.baseUrl ?? process.env.OPENCODE_SERVER;
    if (!url) {
      const spawned = await spawnServe(this.opts.directory);
      this.proc = spawned.proc;
      url = spawned.url;
    }

    const client = createOpencodeClient({ baseUrl: url, directory: this.opts.directory });
    this.client = client;
    const primary = await this.ensurePrimary(client);
    this.primaryId = primary.id;

    // Fixed seats: the boss and Mnemosyne (hr) are always on the floor.
    emit({
      type: "hire",
      employee: { id: primary.id, name: "manager", role: "manager", seat: "manager", sprite: "at-desk" },
    });
    emit({
      type: "hire",
      employee: { id: "hr", name: "hr", role: "hr", seat: "hr", sprite: "at-desk" },
    });

    this.am = await probeAgentmemory(process.env.AGENTMEMORY_URL);
    const board =
      this.am.kind === "actions"
        ? `agentmemory (${this.am.winner})`
        : "in-memory | agentmemory: offline (in-memory board)";
    emit({ type: "status", text: `[grafeio] live - ${url} | board: ${board}` });

    if (this.am.kind === "actions") {
      void this.syncBoard();
      this.poll = setInterval(() => void this.syncBoard(), 5_000);
    }

    const sse = await client.event.subscribe({ query: { directory: this.opts.directory } });
    if (this.stopped) {
      try {
        await sse.stream.return(undefined);
      } catch {
        /* closing anyway */
      }
      return;
    }
    this.stream = sse.stream;
    void this.pump(sse.stream);
  }

  async send(text: string): Promise<void> {
    const emit = this.emitRef;
    const trimmed = text.trim();
    if (!emit || !trimmed) return;

    emit({ type: "chat-user", msg: { id: `user-${++this.chatSeq}`, from: "user", text: trimmed, at: Date.now() } });

    if (!this.client || !this.primaryId || this.stopped) {
      emit({
        type: "chat-boss",
        msg: { id: `boss-${++this.chatSeq}`, from: "boss", text: "[grafeio] backend not started", at: Date.now(), pending: false },
      });
      return;
    }

    // Pending bubble: the completion event re-uses this id so the reducer
    // can swap the bubble in place when the boss' reply lands.
    const pendingId = `boss-${++this.chatSeq}`;
    this.pendingBoss.push(pendingId);
    emit({ type: "chat-boss", msg: { id: pendingId, from: "boss", text: "", at: Date.now(), pending: true } });

    try {
      const { error } = await this.client.session.promptAsync({
        path: { id: this.primaryId },
        query: { directory: this.opts.directory },
        body: { parts: [{ type: "text", text: trimmed }] },
      });
      if (error) throw new Error(errText(error));
    } catch (err) {
      const i = this.pendingBoss.indexOf(pendingId);
      if (i >= 0) this.pendingBoss.splice(i, 1);
      emit({
        type: "chat-boss",
        msg: {
          id: pendingId,
          from: "boss",
          text: `[grafeio] prompt failed: ${shortTitle(errText(err), 120)}`,
          at: Date.now(),
          pending: false,
        },
      });
    }
  }

  async stop(): Promise<void> {
    if (this.stopped) return;
    this.stopped = true;
    for (const t of this.timers) clearTimeout(t);
    this.timers.clear();
    if (this.poll) clearInterval(this.poll);
    const stream = this.stream;
    this.stream = undefined;
    if (stream) {
      try {
        await stream.return(undefined);
      } catch {
        /* best effort */
      }
    }
    const proc = this.proc;
    this.proc = undefined;
    if (proc) {
      try {
        proc.kill("SIGTERM");
      } catch {
        /* already dead */
      }
    }
  }

  // ---------------------------------------------------------------- internals

  private emit(e: OfficeEvent): void {
    if (!this.stopped) this.emitRef?.(e);
  }

  private say(text: string): void {
    this.emit({ type: "status", text });
  }

  /** setTimeout that is tracked for stop() and no-ops after it. */
  private later(ms: number, fn: () => void): void {
    const t = setTimeout(() => {
      this.timers.delete(t);
      if (!this.stopped) fn();
    }, ms);
    this.timers.add(t);
  }

  /** Reuse the newest root session for this directory, else create one. */
  private async ensurePrimary(client: OpencodeClient): Promise<Session> {
    const listed = await client.session.list().catch(() => undefined);
    if (listed && !listed.error && listed.data) {
      const roots = listed.data
        .filter((s) => !s.parentID)
        .sort((a, b) => b.time.created - a.time.created);
      if (roots[0]) return roots[0];
    }
    const created = await client.session.create({
      body: { title: "grafeio office" },
      query: { directory: this.opts.directory },
    });
    if (created.error || !created.data) throw new Error(`session.create failed: ${errText(created.error)}`);
    return created.data;
  }

  /** SSE pump: normalize via events.ts, then run the I/O-needing branches. */
  private async pump(stream: AsyncGenerator<Event>): Promise<void> {
    try {
      for await (const raw of stream) {
        if (this.stopped) break;
        try {
          await this.onEvent(raw);
        } catch (err) {
          this.say(`[grafeio] event handling failed (${raw.type}): ${shortTitle(errText(err), 100)}`);
        }
      }
    } catch (err) {
      if (!this.stopped) this.say(`[grafeio] event stream error: ${shortTitle(errText(err), 100)}`);
    }
    if (!this.stopped) this.say("[grafeio] event stream closed (board/mail continue; restart to re-attach)");
  }

  private async onEvent(raw: Event): Promise<void> {
    for (const e of mapOpencodeEvent(raw, this.ctx, this.primaryId)) this.emit(e);

    if (raw.type === "session.idle") {
      await this.maybeChildReturned(raw.properties.sessionID);
    } else if (raw.type === "session.status" && raw.properties.status.type === "idle") {
      await this.maybeChildReturned(raw.properties.sessionID);
    } else if (raw.type === "message.updated") {
      await this.maybeBossCompleted(raw.properties.info);
    }
  }

  /** Child went idle: a real return only if an assistant text part exists. */
  private async maybeChildReturned(sessionID: string): Promise<void> {
    const client = this.client;
    const employee = this.ctx.employees.get(sessionID);
    if (!client || !employee || this.ctx.returned.has(sessionID)) return;

    const text = await this.latestAssistantText(sessionID);
    if (!text) return; // no assistant output — not a return (abort, rename, ...)

    this.ctx.returned.add(sessionID);
    const prev: BoardTask = this.ctx.tasks.get(sessionID) ?? {
      id: `task-${sessionID}`,
      title: employee.task ?? "untitled brief",
      status: "in-progress",
      owner: employee.name,
      at: Date.now(),
    };
    const done: BoardTask = { ...prev, status: "done" };
    this.ctx.tasks.set(sessionID, done);

    const mail: MailItem = {
      id: `mail-${sessionID}`,
      from: employee.name,
      to: "manager",
      at: Date.now(),
      subject: `return: ${prev.title}`,
      body: text.slice(0, 240),
      kind: "return",
    };
    this.emit({ type: "task", task: done });
    this.emit({ type: "returned", employeeId: sessionID, taskId: done.id, mail });

    // Tidy the org chart: delete the child 10s later (best effort).
    this.later(10_000, () => void this.deleteChild(sessionID));
  }

  /** "child gone" fires on session.deleted (events.ts) OR delete success here. */
  private async deleteChild(sessionID: string): Promise<void> {
    if (!this.client || this.ctx.fired.has(sessionID)) return;
    const res = await this.client.session
      .delete({ path: { id: sessionID }, query: { directory: this.opts.directory } })
      .catch(() => undefined);
    if (res && !res.error && !this.ctx.fired.has(sessionID)) {
      this.ctx.fired.add(sessionID);
      this.emit({ type: "fire", employeeId: sessionID });
      this.ctx.employees.delete(sessionID);
    }
  }

  /** Boss replied: swap the oldest pending bubble for the real reply text. */
  private async maybeBossCompleted(info: Message): Promise<void> {
    if (info.sessionID !== this.primaryId || info.role !== "assistant") return;
    if (!info.time.completed || this.bossCompleted.has(info.id)) return;
    this.bossCompleted.add(info.id);

    const text = await this.latestAssistantText(this.primaryId);
    const id = this.pendingBoss.shift() ?? `boss-${++this.chatSeq}`;
    this.emit({
      type: "chat-boss",
      msg: { id, from: "boss", text: text || "(the boss sent an empty reply)", at: Date.now(), pending: false },
    });
  }

  /** Newest non-empty assistant text part in a session; "" on any failure. */
  private async latestAssistantText(sessionID: string): Promise<string> {
    if (!this.client) return "";
    const res = await this.client.session
      .messages({ path: { id: sessionID }, query: { directory: this.opts.directory } })
      .catch(() => undefined);
    if (!res || res.error || !res.data) return "";
    for (let i = res.data.length - 1; i >= 0; i--) {
      const row = res.data[i];
      if (row.info.role !== "assistant") continue;
      for (const part of row.parts) {
        if (part.type === "text" && part.text.trim().length > 0) return part.text.trim();
      }
    }
    return "";
  }

  /** agentmemory -> board/mail (5s poll; emits only on change). */
  private async syncBoard(): Promise<void> {
    const am = this.am;
    if (!am || am.kind !== "actions" || this.stopped) return;
    const [tasks, mails] = await Promise.all([am.listActions(), am.listMails()]);
    for (const task of tasks) {
      const key = `${task.title}|${task.status}|${task.owner ?? ""}`;
      if (this.amTasks.get(task.id) !== key) {
        this.amTasks.set(task.id, key);
        this.emit({ type: "task", task });
      }
    }
    for (const mail of mails) {
      if (this.amMails.has(mail.id)) continue;
      this.amMails.add(mail.id);
      this.emit({ type: "mail", mail });
    }
  }
}
