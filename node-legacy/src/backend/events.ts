/**
 * events.ts — normalize opencode SSE events into OfficeEvents.
 * Pure helpers only: no I/O, no timers, no UI framework. The live backend
 * (opencode.ts) owns every network call; this module decides WHAT an
 * SSE event means for the office floor, given a mutable context object.
 *
 * SSE -> OfficeEvent mapping table (the backend's contract):
 *
 *   opencode SSE event                     | OfficeEvent(s)                | notes
 *   ---------------------------------------+-------------------------------+----------------------------
 *   session.created (parentID = primary)   | hire + dispatch               | child = new employee;
 *                                          |                               | task in-progress from title
 *   session.updated (known child, title)   | task upsert (retitle)         | server renames after prompt
 *   message.part.updated (child)           | working                       | throttled 500ms/employee
 *   message.updated (child, assistant)     | working                       | typing frames
 *   session.status idle (child)            | [] here                       | needs a messages pull =>
 *                                          |                               | backend emits returned+mail
 *   permission.updated (child)             | blocked {note: title}         | sprite waves at mailbox
 *   permission.replied (child)             | working                       | unblocked, back to typing
 *   session.deleted (child)                | fire                          | deduped vs delete-success
 *   message.updated (primary, assistant,   | [] here                       | needs a messages pull =>
 *     time.completed first seen)           |                               | backend emits chat-boss
 *   session.error (primary)                | chat-boss error line          | boss lane only
 *   anything else                          | []                            | unknown/irrelevant = silence
 */
import type { Event, Session } from "@opencode-ai/sdk";
import type {
  BoardTask,
  Employee,
  EmployeeRole,
  OfficeEvent,
} from "../state.js";

/** Mutable reducer-side context. Pure (no I/O) — the backend owns fetches. */
export interface NormCtx {
  /** child session id -> employee record */
  employees: Map<string, Employee>;
  /** child session id -> its board task */
  tasks: Map<string, BoardTask>;
  /** role -> last issued number (tekton-1, tekton-2, ...) */
  nameCounts: Map<EmployeeRole, number>;
  /** logical seat counter */
  seatSeq: number;
  /** child id -> last "working" emit time (throttle) */
  lastWorkingAt: Map<string, number>;
  /** child sessions that already returned (no more working pulses) */
  returned: Set<string>;
  /** child sessions already fired (dedupe delete-event vs delete-call) */
  fired: Set<string>;
}

export function createNormCtx(): NormCtx {
  return {
    employees: new Map(),
    tasks: new Map(),
    nameCounts: new Map(),
    seatSeq: 0,
    lastWorkingAt: new Map(),
    returned: new Set(),
    fired: new Set(),
  };
}

/** Greek-desk naming per role (state.ts canon). */
function nameBase(role: EmployeeRole): string {
  switch (role) {
    case "scout": return "skopos";
    case "reviewer": return "dikastes";
    case "runner": return "hemerodromos";
    case "hr": return "hr";
    case "manager": return "manager";
    default: return "tekton";
  }
}

/**
 * Guess a role from the child session's title (plus an optional agent
 * hint if the backend can pair one later). Machine-generated titles,
 * not member language — plain substring rules are the right tool here.
 */
export function roleFromSession(
  session: Pick<Session, "title" | "parentID">,
  agentHint?: string,
): EmployeeRole {
  const hay = `${agentHint ?? ""} ${session.title ?? ""}`.toLowerCase();
  if (hay.includes("explore") || hay.includes("scout") || hay.includes("skopos")) return "scout";
  if (hay.includes("review") || hay.includes("dikastes")) return "reviewer";
  if (hay.includes("runner") || hay.includes("hemerodromos")) return "runner";
  return "developer";
}

/** Collapse whitespace, bound length, keep it ASCII-ish for the floor. */
export function shortTitle(s: string, max = 48): string {
  const flat = s.replace(/\s+/g, " ").trim();
  if (flat.length === 0) return "untitled brief";
  return flat.length > max ? `${flat.slice(0, max - 3).trimEnd()}...` : flat;
}

function issueEmployee(ctx: NormCtx, session: Session): Employee {
  const role = roleFromSession(session);
  const n = (ctx.nameCounts.get(role) ?? 0) + 1;
  ctx.nameCounts.set(role, n);
  const employee: Employee = {
    id: session.id, // subagent session id IS the employee id
    name: `${nameBase(role)}-${n}`,
    role,
    seat: `desk-${++ctx.seatSeq}`,
    sprite: "to-manager", // dispatch walk starts immediately
    task: shortTitle(session.title || "untitled brief"),
  };
  ctx.employees.set(session.id, employee);
  return employee;
}

function issueTask(ctx: NormCtx, session: Session, owner: string): BoardTask {
  const task: BoardTask = {
    id: `task-${session.id}`,
    title: shortTitle(session.title || "untitled brief"),
    status: "in-progress",
    owner,
    at: Date.now(),
  };
  ctx.tasks.set(session.id, task);
  return task;
}

/**
 * The one pure mapping entry point. `primaryId` identifies the boss
 * session; everything with parentID === primaryId is an employee.
 */
export function mapOpencodeEvent(raw: Event, ctx: NormCtx, primaryId: string): OfficeEvent[] {
  switch (raw.type) {
    case "session.created": {
      const info = raw.properties.info;
      if (info.parentID !== primaryId || ctx.employees.has(info.id)) return [];
      const employee = issueEmployee(ctx, info);
      const task = issueTask(ctx, info, employee.name);
      return [
        { type: "hire", employee },
        { type: "dispatch", task, employeeId: employee.id },
      ];
    }

    case "session.updated": {
      // Title often lands after creation; keep the board row honest.
      const info = raw.properties.info;
      const task = ctx.tasks.get(info.id);
      if (!task) return [];
      const title = shortTitle(info.title || "");
      if (title === "untitled brief" || title === task.title) return [];
      const next = { ...task, title };
      ctx.tasks.set(info.id, next);
      return [{ type: "task", task: next }];
    }

    case "message.part.updated": {
      const part = raw.properties.part;
      const employee = ctx.employees.get(part.sessionID);
      if (!employee || ctx.returned.has(part.sessionID)) return [];
      return throttledWorking(ctx, employee.id, ctx.tasks.get(part.sessionID)?.id);
    }

    case "message.updated": {
      const info = raw.properties.info;
      if (info.sessionID === primaryId) return []; // boss completion needs a fetch — backend handles it
      const employee = ctx.employees.get(info.sessionID);
      if (!employee || info.role !== "assistant" || ctx.returned.has(info.sessionID)) return [];
      return throttledWorking(ctx, employee.id, ctx.tasks.get(info.sessionID)?.id);
    }

    case "permission.updated": {
      const p = raw.properties;
      const employee = ctx.employees.get(p.sessionID);
      if (!employee) return [];
      return [{ type: "blocked", employeeId: employee.id, note: shortTitle(p.title || "permission needed", 60) }];
    }

    case "permission.replied": {
      const employee = ctx.employees.get(raw.properties.sessionID);
      if (!employee || ctx.returned.has(raw.properties.sessionID)) return [];
      return throttledWorking(ctx, employee.id, ctx.tasks.get(raw.properties.sessionID)?.id, true);
    }

    case "session.deleted": {
      const info = raw.properties.info;
      if (!ctx.employees.has(info.id) || ctx.fired.has(info.id)) return [];
      ctx.fired.add(info.id);
      return [{ type: "fire", employeeId: info.id }];
    }

    case "session.error": {
      const p = raw.properties;
      if (p.sessionID !== primaryId) return [];
      const message =
        p.error && "data" in p.error && p.error.data && "message" in p.error.data
          ? String(p.error.data.message)
          : "unknown error";
      return [{
        type: "chat-boss",
        msg: {
          id: `boss-error-${Date.now()}`,
          from: "boss",
          text: `[grafeio] boss error: ${shortTitle(message, 120)}`,
          at: Date.now(),
          pending: false,
        },
      }];
    }

    default:
      return [];
  }
}

/** Working pulses animate typing; one per 500ms per employee is plenty. */
function throttledWorking(
  ctx: NormCtx,
  employeeId: string,
  taskId: string | undefined,
  force = false,
): OfficeEvent[] {
  const now = Date.now();
  const last = ctx.lastWorkingAt.get(employeeId) ?? 0;
  if (!force && now - last < 500) return [];
  ctx.lastWorkingAt.set(employeeId, now);
  return [{ type: "working", employeeId, taskId }];
}
