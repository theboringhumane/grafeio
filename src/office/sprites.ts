/**
 * sprites.ts — animated glyphs + walker state machine.
 * Pure unit logic, deterministic: same tick + same event stream -> same frame.
 *
 * Positions are tracked per employee id in a module-local map (OfficeState's
 * Employee shape is fixed and cannot carry x/y). The map is pruned on fire,
 * seeded on first sight, and is the only mutable thing here. Walkers live
 * against the CURRENT plan (floorplan.currentPlan): when the plan generation
 * changes (terminal resized), walkers are clamped back onto the new floor and
 * re-walk to their (recomputed) targets.
 */
import type {EmployeeRole, OfficeState, SpriteState} from "../state.js";
import {currentPlan, type ColorName, type Plan, type Point} from "./floorplan.js";
import {seatAnchor} from "./roster.js";

export const ROLE_GLYPH: Record<EmployeeRole, string> = {
  manager: "M",
  hr: "H",
  developer: "T",
  scout: "S",
  reviewer: "D",
  runner: "R",
};

/** Sprite paint per role — matches the app legend (M yellow, H red, ...). */
export const ROLE_COLOR: Record<EmployeeRole, ColorName> = {
  manager: "yellow",
  hr: "red",
  developer: "cyan",
  scout: "green",
  reviewer: "magenta",
  runner: "blue",
};

const COFFEE_TICKS = 60; // how long a break lasts

/** Animated 3-char glyph for a role+state at a given tick. */
export function spriteFrame(role: EmployeeRole, sprite: SpriteState, tick: number): string {
  const L = ROLE_GLYPH[role];
  const beat = tick % 2;
  switch (sprite) {
    case "working": // typing arms
      return beat ? `_${L}_` : `~${L}~`;
    case "to-manager":
    case "to-desk":
    case "to-coffee": // walking bob
      return beat ? `(${L})` : ` ${L} `;
    case "meeting": // talking
      return beat ? ` ${L}.` : ` ${L} `;
    case "at-mailbox": // waving for attention (blocked)
      return beat ? `\\${L} ` : ` ${L}/`;
    case "coffee": // sipping, steam wisp
      return beat ? ` ${L}~` : ` ${L} `;
    default: // at-desk: idle; sleep-z's float above (idleBlinkZs), NEVER glued into this row
      return ` ${L} `;
  }
}

/**
 * Floating sleep-z's for an idling (at-desk) sprite on its blink frames:
 * "z" on the first blink frame (tick % 16 === 0), "zZ" on the deeper one
 * (tick % 16 === 1), null otherwise. floor.tsx stamps these one row above
 * the sprite's right shoulder at (x+2, y-1) — never inside the sprite's own
 * row, where they glue into the role letter and read as typos ("zMz").
 */
export function idleBlinkZs(sprite: SpriteState, tick: number): string | null {
  if (sprite !== "at-desk") return null;
  const phase = ((tick % 16) + 16) % 16;
  if (phase === 0) return "z";
  if (phase === 1) return "zZ";
  return null;
}

interface Walker {
  x: number;
  y: number;
  since: number; // tick when the current parked state started
  gen: number; // plan generation this position was validated against
}

const walkers = new Map<string, Walker>();

/** Current pixel position of an employee (undefined until first advance). */
export function spritePosition(id: string): Point | undefined {
  return walkers.get(id);
}

function targetFor(sprite: SpriteState, seat: string, plan: Plan): Point {
  switch (sprite) {
    case "to-manager":
    case "meeting":
      return plan.hotspots.meet;
    case "to-coffee":
    case "coffee":
      return plan.hotspots.tea;
    case "at-mailbox":
      return plan.hotspots.mail;
    default: // at-desk / working / to-desk head home
      return seatAnchor(seat);
  }
}

/** Advance every walker by up to 2 cells (dogleg: x first, then y); drive state transitions. */
export function advanceSprites(state: OfficeState): OfficeState {
  const plan = currentPlan();
  const live = new Set(state.employees.map((e) => e.id));
  for (const id of [...walkers.keys()]) if (!live.has(id)) walkers.delete(id);

  let changed = false;
  const employees = state.employees.map((e) => {
    let w = walkers.get(e.id);
    if (!w) {
      const a = seatAnchor(e.seat);
      w = {x: a.x, y: a.y, since: state.tick, gen: plan.gen};
      walkers.set(e.id, w);
    }
    if (w.gen !== plan.gen) {
      // plan resized: clamp back onto the new floor, then re-walk to target
      w.x = Math.min(Math.max(1, w.x), plan.width - 2);
      w.y = Math.min(Math.max(1, w.y), plan.height - 2);
      w.gen = plan.gen;
    }
    const t = targetFor(e.sprite, e.seat, plan);
    if (w.x !== t.x) w.x += Math.sign(t.x - w.x) * Math.min(2, Math.abs(t.x - w.x));
    else if (w.y !== t.y) w.y += Math.sign(t.y - w.y) * Math.min(2, Math.abs(t.y - w.y));

    const arrived = w.x === t.x && w.y === t.y;
    let sprite = e.sprite;
    if (sprite === "to-manager" && arrived) sprite = "meeting";
    else if (sprite === "to-coffee" && arrived) {
      sprite = "coffee";
      w.since = state.tick;
    } else if (sprite === "to-desk" && arrived) sprite = "at-desk";
    else if (sprite === "coffee" && state.tick - w.since >= COFFEE_TICKS) sprite = "to-desk";

    if (sprite !== e.sprite) {
      changed = true;
      return {...e, sprite};
    }
    return e;
  });
  return changed ? {...state, employees} : state;
}
