/**
 * roster.ts — seat assignment, resolved against the CURRENT floor plan
 * (geometry moved to floorplan.ts; seats are no longer fixed pixels).
 *
 * Seat ids: manager | hr (cabin-1) | cabin-2 | cabin-3 | dev-1..N |
 * scout-1..2 | treadmill-1 | floor-<n> (overflow standing near the break area).
 */
import type {EmployeeRole} from "../state.js";
import {currentPlan, type Plan, type Point} from "./floorplan.js";

/** Dev seat ids present in the plan, sorted numerically: dev-1, dev-2, ... */
function devSeats(plan: Plan): string[] {
  return [...plan.anchors.keys()]
    .filter((k) => /^dev-\d+$/.test(k)) // machine-format seat ids, not NL
    .sort((a, b) => Number(a.slice(4)) - Number(b.slice(4)));
}

/** Seat candidates per role, in fill order, against a plan. */
export function roleSeats(role: EmployeeRole, plan: Plan = currentPlan()): string[] {
  switch (role) {
    case "manager":
      return ["manager"];
    case "hr":
      return ["hr"]; // cabin-1, the HR cabin
    case "reviewer":
      return ["cabin-2"]; // dikastes
    case "runner":
      return ["treadmill-1"]; // hemerodromos, in the server room
    case "scout":
      return ["scout-1", "scout-2"]; // right-side pods of the dev field
    default:
      return devSeats(plan); // tekton devs, in pod order
  }
}

/** First free seat for the role; overflow -> "floor-<n>" standing spots. */
export function assignSeat(taken: Iterable<string>, role: EmployeeRole): string {
  const used = new Set(taken);
  for (const s of roleSeats(role)) if (!used.has(s)) return s;
  let n = 0;
  while (used.has(`floor-${n}`)) n++;
  return `floor-${n}`;
}

/** Anchor point of a seat id. Unknown / overflow seats stand near the break area. */
export function seatAnchor(seat: string): Point {
  const plan = currentPlan();
  const a = plan.anchors.get(seat);
  if (a) return a;
  const m = /^floor-(\d+)$/.exec(seat); // machine format, not NL
  const n = m ? Number(m[1]) : 0;
  const o = plan.hotspots.overflow;
  const x = Math.min(Math.max(1, o.x - (n % 4) * 3), plan.width - 3);
  const y = Math.min(Math.max(1, o.y - Math.floor(n / 4) * 2), plan.height - 2);
  return {x, y};
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
