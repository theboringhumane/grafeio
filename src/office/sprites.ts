/**
 * sprites.ts — animated glyphs + walker state machine.
 * Pure unit logic, deterministic: same tick + same event stream -> same frame.
 *
 * Positions are tracked per employee id in a module-local map (OfficeState's
 * Employee shape is fixed and cannot carry x/y). The map is pruned on fire,
 * seeded on first sight, and is the only mutable thing here.
 */
import type {EmployeeRole, OfficeState, SpriteState} from "../state.js";
import {seatAnchor} from "./roster.js";

export const ROLE_GLYPH: Record<EmployeeRole, string> = {
  manager: "M",
  hr: "H",
  developer: "T",
  scout: "S",
  reviewer: "D",
  runner: "R",
};

// Standing targets for non-desk states.
const MEET = {x: 32, y: 4}; // in front of the boss desk
const MAIL = {x: 42, y: 3}; // at the mail tray, waving
const TEA = {x: 60, y: 3}; // at the tea machine
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
    default: // at-desk: idle z-z blink
      return tick % 16 < 2 ? `z${L}z` : ` ${L} `;
  }
}

interface Walker {
  x: number;
  y: number;
  since: number; // tick when the current parked state started
}

const walkers = new Map<string, Walker>();

/** Current pixel position of an employee (undefined until first advance). */
export function spritePosition(id: string): Walker | undefined {
  return walkers.get(id);
}

function targetFor(sprite: SpriteState, seat: string): {x: number; y: number} {
  switch (sprite) {
    case "to-manager":
    case "meeting":
      return MEET;
    case "to-coffee":
    case "coffee":
      return TEA;
    case "at-mailbox":
      return MAIL;
    default: // at-desk / working / to-desk head home
      return seatAnchor(seat);
  }
}

/** Advance every walker by up to 2 cells (dogleg: x first, then y); drive state transitions. */
export function advanceSprites(state: OfficeState): OfficeState {
  const live = new Set(state.employees.map((e) => e.id));
  for (const id of [...walkers.keys()]) if (!live.has(id)) walkers.delete(id);

  let changed = false;
  const employees = state.employees.map((e) => {
    let w = walkers.get(e.id);
    if (!w) {
      w = {...seatAnchor(e.seat), since: state.tick};
      walkers.set(e.id, w);
    }
    const t = targetFor(e.sprite, e.seat);
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
