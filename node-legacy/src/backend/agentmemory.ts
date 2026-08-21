/**
 * agentmemory.ts — HTTP adapter for the agentmemory server (task board +
 * mail). One startup probe decides the mode; failures degrade silently to
 * "none", where the backend derives board+mail from opencode events.
 * This module NEVER throws: every fetch is bounded (2s) and wrapped.
 */
import type { BoardTask, MailItem, TaskStatus } from "../state.js";

export type AgentmemoryKind = "actions" | "none";

export interface AgentmemoryHandle {
  kind: AgentmemoryKind;
  baseUrl: string;
  /** which probe endpoint won, for status/logging (e.g. "GET /agentmemory/actions") */
  winner: string;
  /** board rows; [] in "none" mode */
  listActions(): Promise<BoardTask[]>;
  /** mail items; [] in "none" mode */
  listMails(): Promise<MailItem[]>;
}

const DEFAULT_BASE = "http://localhost:3111";
const PROBE_TIMEOUT_MS = 2_000;

// Probe order from the backend spec: first 2xx JSON wins the board lane.
// The signals lane needs ?agentId= (bare /agentmemory/signals is a 400
// on the live server), so it carries its default query.
const BOARD_CANDIDATES = ["/agentmemory/actions", "/agentmemory/frontier", "/agentmemory/mail"];
const MAIL_CANDIDATES = ["/agentmemory/signals?agentId=grafeio", "/agentmemory/mail"];

/** GET with a hard timeout; returns parsed JSON or undefined. */
async function getJson(url: string): Promise<unknown | undefined> {
  try {
    const res = await fetch(url, { signal: AbortSignal.timeout(PROBE_TIMEOUT_MS) });
    if (!res.ok) return undefined;
    return (await res.json()) as unknown;
  } catch {
    return undefined;
  }
}

async function probe(candidates: string[], base: string): Promise<string | undefined> {
  for (const path of candidates) {
    const json = await getJson(`${base}${path}`);
    if (json !== undefined) return path;
  }
  return undefined;
}

/** First array found under any of the given keys (or a top-level array). */
function pickArray(json: unknown, keys: string[]): Array<Record<string, unknown>> {
  if (Array.isArray(json)) return json.filter((x) => x && typeof x === "object") as Array<Record<string, unknown>>;
  if (json && typeof json === "object") {
    for (const k of keys) {
      const v = (json as Record<string, unknown>)[k];
      if (Array.isArray(v)) return v.filter((x) => x && typeof x === "object") as Array<Record<string, unknown>>;
    }
  }
  return [];
}

function taskStatus(raw: unknown): TaskStatus {
  const s = String(raw ?? "").toLowerCase();
  if (["in-progress", "in_progress", "active", "leased", "doing"].includes(s)) return "in-progress";
  if (["done", "completed", "complete", "cancelled", "closed"].includes(s)) return "done";
  return "pending";
}

function epochMs(raw: unknown): number {
  const n = typeof raw === "number" ? raw : Date.parse(String(raw ?? ""));
  return Number.isFinite(n) ? n : Date.now();
}

/** actions/frontier rows -> BoardTask[]. Field names are best-effort. */
function normalizeTasks(json: unknown): BoardTask[] {
  const rows = pickArray(json, ["actions", "items", "data"]).map((row) =>
    row.action && typeof row.action === "object" ? (row.action as Record<string, unknown>) : row,
  );
  const tasks: BoardTask[] = [];
  for (const row of rows) {
    const id = String(row.id ?? "");
    if (!id) continue;
    tasks.push({
      id,
      title: String(row.title ?? row.name ?? "(untitled action)").slice(0, 80),
      status: taskStatus(row.status),
      owner: typeof row.createdBy === "string" && row.createdBy !== "unknown" ? row.createdBy : undefined,
      at: epochMs(row.createdAt ?? row.updatedAt),
    });
  }
  return tasks;
}

/** signals rows -> MailItem[]. The live schema is loose; map defensively. */
function normalizeMails(json: unknown): MailItem[] {
  const rows = pickArray(json, ["signals", "items", "data", "mail"]);
  const mails: MailItem[] = [];
  for (const row of rows) {
    const body = String(row.content ?? row.body ?? row.text ?? "").slice(0, 240);
    if (!body) continue;
    const kind = String(row.type ?? row.kind ?? "").toLowerCase();
    mails.push({
      id: String(row.id ?? row.signalId ?? `sig-${mails.length}`),
      from: String(row.from ?? row.sender ?? "agentmemory"),
      to: String(row.to ?? row.agentId ?? "manager"),
      at: epochMs(row.createdAt ?? row.at),
      subject: String(row.subject ?? row.type ?? row.name ?? "signal").slice(0, 80),
      body,
      kind: kind.includes("return") ? "return" : kind.includes("brief") ? "brief" : "notice",
    });
  }
  return mails;
}

/**
 * Probe the agentmemory server once. Winner endpoint is logged via the
 * returned handle's `winner` field (surfaced in the backend status line —
 * console output would corrupt the Ink render). Falls back to "none".
 */
export async function probeAgentmemory(base: string = DEFAULT_BASE): Promise<AgentmemoryHandle> {
  const baseUrl = base.replace(/\/$/, "");
  let boardLane: string | undefined;
  let mailLane: string | undefined;
  try {
    boardLane = await probe(BOARD_CANDIDATES, baseUrl);
    mailLane = await probe(MAIL_CANDIDATES, baseUrl);
  } catch {
    // unreachable host, DNS, etc. — degrade silently
  }
  if (!boardLane) {
    return {
      kind: "none",
      baseUrl,
      winner: "none (agentmemory unreachable)",
      listActions: async () => [],
      listMails: async () => [],
    };
  }
  return {
    kind: "actions",
    baseUrl,
    winner: `GET ${boardLane}`,
    listActions: async () => {
      const json = await getJson(`${baseUrl}${boardLane}`);
      return json === undefined ? [] : normalizeTasks(json);
    },
    listMails: async () => {
      if (!mailLane) return [];
      const json = await getJson(`${baseUrl}${mailLane}`);
      return json === undefined ? [] : normalizeMails(json);
    },
  };
}
