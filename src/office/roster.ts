/**
 * roster.ts — seat assignment + floor coordinates.
 * Pure data + tiny helpers. The ONE source for where seats/desks live.
 */
import type {EmployeeRole} from "../state.js";

export interface SeatDef {
  anchor: {x: number; y: number}; // where the employee sprite stands when at this seat
  desk: {x: number; y: number; glyph: string} | null; // desk furniture
  label: {x: number; y: number} | null; // name label under the desk
}

const seat = (
  ax: number,
  ay: number,
  dx: number,
  dy: number,
  glyph: string,
  lx: number,
  ly: number,
): SeatDef => ({anchor: {x: ax, y: ay}, desk: {x: dx, y: dy, glyph}, label: {x: lx, y: ly}});

export const SEATS: Record<string, SeatDef> = {
  manager: seat(32, 1, 29, 2, "[=BOSS=]", 30, 3),
  hr: seat(4, 4, 2, 5, "[=====]", 2, 6),
  "dev-1": seat(10, 6, 7, 7, "[=====]", 7, 8),
  "dev-2": seat(22, 6, 19, 7, "[=====]", 19, 8),
  "dev-3": seat(10, 10, 7, 11, "[=====]", 7, 12),
  "dev-4": seat(22, 10, 19, 11, "[=====]", 19, 12),
  "scout-1": seat(40, 6, 37, 7, "[=====]", 37, 8),
  "scout-2": seat(52, 6, 49, 7, "[=====]", 49, 8),
  "bench-1": seat(40, 10, 37, 11, "[=====]", 37, 12),
  "treadmill-1": seat(52, 10, 49, 11, "[o==o]", 49, 12),
};

const ROLE_SEATS: Record<EmployeeRole, string[]> = {
  manager: ["manager"],
  hr: ["hr"],
  developer: ["dev-1", "dev-2", "dev-3", "dev-4"],
  scout: ["scout-1", "scout-2"],
  reviewer: ["bench-1"],
  runner: ["treadmill-1"],
};

/** First free seat for the role; overflow -> "floor-<n>" standing spots. */
export function assignSeat(taken: Iterable<string>, role: EmployeeRole): string {
  const used = new Set(taken);
  for (const s of ROLE_SEATS[role]) if (!used.has(s)) return s;
  let n = 0;
  while (used.has(`floor-${n}`)) n++;
  return `floor-${n}`;
}

/** Anchor point of a seat id. Unknown / overflow seats stand along the bottom row. */
export function seatAnchor(seat: string): {x: number; y: number} {
  const def = SEATS[seat];
  if (def) return def.anchor;
  const m = /^floor-(\d+)$/.exec(seat); // machine format, not NL
  const n = m ? Math.min(Number(m[1]), 8) : 0;
  return {x: 24 + n * 4, y: 17};
}

/** Panel color for an employee/agent name (boss yellow, dev cyan, ...). */
export function nameColor(name: string): string {
  const n = name.toLowerCase();
  if (n.startsWith("boss") || n.startsWith("manager")) return "yellow";
  if (n.startsWith("hr")) return "red";
  if (n.startsWith("tekton") || n.startsWith("dev")) return "cyan";
  if (n.startsWith("skopos") || n.startsWith("scout")) return "green";
  if (n.startsWith("dikastes") || n.startsWith("review")) return "magenta";
  if (n.startsWith("hemero") || n.startsWith("run")) return "blue";
  return "white";
}
