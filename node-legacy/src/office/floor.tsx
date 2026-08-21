/**
 * floor.tsx — the office floor as a STYLED char grid, DYNAMICALLY sized.
 * buildGridRows() is the primary renderer — PURE: (state, width, height) ->
 * exactly `height` RowSegments, and concat(segments.text) is exactly `width`
 * chars per row. buildGridPlain() joins the same segments, so the plain
 * layout and the colored layout can never drift apart. Ink only paints.
 */
import React from "react";
import {Box, Text} from "ink";
import type {EmployeeRole, OfficeState, SpriteState} from "../state.js";
import {computePlan, tickClock, type ColorName, type Plan, type Zone} from "./floorplan.js";
import {seatAnchor} from "./roster.js";
import {ROLE_COLOR, idleBlinkZs, spriteFrame, spritePosition} from "./sprites.js";

export const LEGEND = "M=boss H=hr T=dev S=scout D=rev R=run [tea]=break";

/** One styled run of characters inside a row. */
export interface RowSegment {
  text: string;
  color?: ColorName;
  bold?: boolean;
  dim?: boolean;
}

/** A grid row as merged styled runs; concat(segments.text) === one row, exactly. */
export interface RowSegments {
  segments: RowSegment[];
}

// ---------------------------------------------------------------------------
// styled cell grid internals
// ---------------------------------------------------------------------------

interface Style {
  color?: ColorName;
  bold?: boolean;
  dim?: boolean;
}

interface Cell extends Style {
  ch: string;
}

const BLANK: Cell = {ch: " "};

function put(g: Cell[][], W: number, H: number, x: number, y: number, s: string, style?: Style): void {
  if (y < 0 || y >= H) return;
  for (let i = 0; i < s.length; i++) {
    const xx = x + i;
    if (xx >= 0 && xx < W) g[y][xx] = {ch: s[i], ...style};
  }
}

/** Repaint existing cells in place (keeps their chars). */
function restyle(g: Cell[][], W: number, H: number, x: number, y: number, n: number, style: Style): void {
  if (y < 0 || y >= H) return;
  for (let i = 0; i < n; i++) {
    const xx = x + i;
    if (xx >= 0 && xx < W) g[y][xx] = {...g[y][xx], ...style};
  }
}

/** Walled room: "+" corners, wall char for the sides, skipped cells at door gaps. */
function drawZone(g: Cell[][], W: number, H: number, z: Zone): void {
  const style: Style = {color: z.color ?? "gray"};
  const wall = z.wall === "-" ? "-" : z.wall;
  const inDoor = (side: "n" | "s" | "e" | "w", i: number): boolean =>
    z.doors.some((d) => d.side === side && i >= d.at && i < d.at + d.size);
  for (let dx = 0; dx < z.w; dx++) {
    if (!inDoor("n", dx)) put(g, W, H, z.x + dx, z.y, dx === 0 || dx === z.w - 1 ? "+" : wall, style);
    if (!inDoor("s", dx)) put(g, W, H, z.x + dx, z.y + z.h - 1, dx === 0 || dx === z.w - 1 ? "+" : wall, style);
  }
  for (let dy = 1; dy < z.h - 1; dy++) {
    if (!inDoor("w", dy)) put(g, W, H, z.x, z.y + dy, z.wall === "-" ? "|" : wall, style);
    if (!inDoor("e", dy)) put(g, W, H, z.x + z.w - 1, z.y + dy, z.wall === "-" ? "|" : wall, style);
  }
}

const BUBBLE_W = 16; // 14 text cols + "|" borders
const BUBBLE_BORDER: Style = {color: "gray"};
const BUBBLE_TEXT: Style = {color: "white"};

function centerPad(t: string, n: number): string {
  const room = Math.max(0, n - t.length);
  const l = Math.floor(room / 2);
  return " ".repeat(l) + t + " ".repeat(room - l);
}

/**
 * ".--------------." / "|   big day.   |" / "+--*-----------+" three rows
 * directly above the sprite. Clipped at the grid top (colliding rows drop).
 * Borders + trailing pointer gray, inner text white.
 */
function drawBubble(g: Cell[][], W: number, H: number, text: string, cx: number, cy: number): void {
  const t = centerPad(text.slice(0, 14), 14);
  const x = Math.max(0, Math.min(Math.max(0, W - BUBBLE_W), cx - Math.floor(BUBBLE_W / 2)));
  const ry = (i: number) => cy - 3 + i;
  if (ry(0) >= 1) put(g, W, H, x, ry(0), "." + "-".repeat(14) + ".", BUBBLE_BORDER);
  if (ry(1) >= 1) {
    put(g, W, H, x, ry(1), "|", BUBBLE_BORDER);
    put(g, W, H, x + 1, ry(1), t, BUBBLE_TEXT);
    put(g, W, H, x + BUBBLE_W - 1, ry(1), "|", BUBBLE_BORDER);
  }
  if (ry(2) >= 1) put(g, W, H, x, ry(2), "+" + "--*" + "-".repeat(11) + "+", BUBBLE_BORDER);
}

/** Sprite paint: role color (legend), red bold when blocked, yellow on coffee. */
function spriteStyle(role: EmployeeRole, sprite: SpriteState): Style {
  if (sprite === "at-mailbox") return {color: "red", bold: true}; // blocked: waving for attention
  if (sprite === "coffee") return {color: "yellow"}; // sipping
  return {color: ROLE_COLOR[role]}; // walking/working/idle keep role color, never dim
}

const POD_SEAT = /^(?:dev|scout)-\d+$/; // machine-format seat ids, not NL
const CHAIR_SEAT = /^(?:hr|cabin-\d+|dev-\d+|scout-\d+)$/; // seats whose anchor is a "(_)" chair

/**
 * Is (x,y) a structural cell: out of grid, the outer border, or a zone wall
 * (door gaps excluded)? Used to keep floating sleep-z's off buildings —
 * walls and bubbles are the named blockers; furniture glyphs may be
 * transiently overlaid by the blink animation (it is gray and 2/16 ticks).
 */
function isStructural(plan: Plan, W: number, H: number, x: number, y: number): boolean {
  if (x < 0 || x >= W || y < 0 || y >= H) return true;
  if (W >= 2 && H >= 2 && (y === 0 || y === H - 1 || x === 0 || x === W - 1)) return true;
  for (const z of plan.zones) {
    if (x < z.x || x >= z.x + z.w || y < z.y || y >= z.y + z.h) continue;
    const onN = y === z.y;
    const onS = y === z.y + z.h - 1;
    const onW = x === z.x;
    const onE = x === z.x + z.w - 1;
    const inDoor = (side: "n" | "s" | "e" | "w", i: number): boolean =>
      z.doors.some((d) => d.side === side && i >= d.at && i < d.at + d.size);
    if ((onN || onS) && !inDoor(onN ? "n" : "s", x - z.x)) return true;
    if ((onW || onE) && y > z.y && y < z.y + z.h - 1 && !inDoor(onW ? "w" : "e", y - z.y)) return true;
  }
  return false;
}

/** Merge runs of identically-styled cells into row segments. */
function mergeRow(row: Cell[]): RowSegments {
  const segments: RowSegment[] = [];
  for (const cell of row) {
    const last = segments[segments.length - 1];
    if (last && last.color === cell.color && !!last.bold === !!cell.bold && !!last.dim === !!cell.dim) {
      last.text += cell.ch;
    } else {
      segments.push({
        text: cell.ch,
        ...(cell.color ? {color: cell.color} : {}),
        ...(cell.bold ? {bold: true} : {}),
        ...(cell.dim ? {dim: true} : {}),
      });
    }
  }
  return {segments};
}

/**
 * PRIMARY renderer: the floor as styled rows. PURE.
 * Exactly `height` entries; concat(segments.text) is exactly `width` chars per row.
 */
export function buildGridRows(state: OfficeState, width: number, height: number): RowSegments[] {
  const W = Math.max(1, Math.floor(width));
  const H = Math.max(1, Math.floor(height));
  const plan = computePlan(width, height);
  const g = Array.from({length: H}, () => new Array<Cell>(W).fill(BLANK));

  // outer border
  if (W >= 2 && H >= 2) {
    const border: Style = {color: "gray"};
    put(g, W, H, 0, 0, "+" + "-".repeat(W - 2) + "+", border);
    put(g, W, H, 0, H - 1, "+" + "-".repeat(W - 2) + "+", border);
    for (let y = 1; y < H - 1; y++) {
      put(g, W, H, 0, y, "|", border);
      put(g, W, H, W - 1, y, "|", border);
    }
  } else {
    for (let y = 0; y < H; y++) put(g, W, H, 0, y, "-".repeat(W), {color: "gray"});
  }

  // furniture first, walls over any spill, clock on the boss-office wall;
  // `over` props (window) go last so the zone walls don't erase them
  for (const p of plan.props) if (!p.over) put(g, W, H, p.x, p.y, p.glyph, {color: p.color, bold: p.bold});
  for (const z of plan.zones) drawZone(g, W, H, z);
  for (const p of plan.props) if (p.over) put(g, W, H, p.x, p.y, p.glyph, {color: p.color, bold: p.bold});
  put(g, W, H, plan.hotspots.clock.x, plan.hotspots.clock.y, tickClock(state.tick), {color: "white"});

  // boss nameplate is a STATUS line: typing when a boss chat answer is
  // pending, meetin while anyone is at the boss desk, awaiting otherwise
  const plate = state.employees.some((e) => e.sprite === "meeting")
    ? "[meetin]"
    : state.chat.some((m) => m.from === "boss" && m.pending)
      ? "[typing]"
      : "[awaiting]";
  put(g, W, H, plan.nameplate.x, plan.nameplate.y, plate.padEnd(10), {color: "yellow", bold: true});

  // degrade badge (drawn once, centered)
  if (plan.tiny) {
    const badge = "small terminal";
    put(g, W, H, Math.max(0, Math.floor((W - badge.length) / 2)), Math.floor(H / 2), badge);
  }

  // animated sprites, stamped over the floor
  const occupied = new Set<string>(); // sprite cells: "x,y" -> someone sits/stands here
  for (const e of state.employees) {
    const p = spritePosition(e.id) ?? seatAnchor(e.seat);
    for (let dx = 0; dx < 3; dx++) occupied.add(`${p.x + dx},${p.y}`);
    put(g, W, H, p.x, p.y, spriteFrame(e.role, e.sprite, state.tick), spriteStyle(e.role, e.sprite));
  }

  // floating sleep-z's: one row above an idling sprite's right shoulder at
  // (x+2, y-1) — NEVER glued into the sprite's own row (zMz reads as a typo,
  // not as a sleeping worker). Skipped off-grid, onto a wall, or onto another
  // sprite; bubbles are drawn later and simply overwrite any z under them.
  for (const e of state.employees) {
    const zs = idleBlinkZs(e.sprite, state.tick);
    if (!zs) continue;
    const p = spritePosition(e.id) ?? seatAnchor(e.seat);
    const zx = p.x + 2;
    const zy = p.y - 1;
    let blocked = false;
    for (let i = 0; i < zs.length; i++) {
      if (isStructural(plan, W, H, zx + i, zy) || occupied.has(`${zx + i},${zy}`)) {
        blocked = true;
        break;
      }
    }
    if (!blocked) put(g, W, H, zx, zy, zs, {color: "gray"});
  }

  // empty seats read EMPTY: a seat-anchored chair stays green only while
  // some employee's sprite actually covers its anchor cell; otherwise gray+dim
  for (const [seat, a] of plan.anchors) {
    if (!CHAIR_SEAT.test(seat)) continue;
    let free = true;
    for (let dx = 0; dx < 3 && free; dx++) if (occupied.has(`${a.x + dx},${a.y}`)) free = false;
    if (free) restyle(g, W, H, a.x, a.y, 3, {color: "gray", dim: true});
  }

  // lit screens: a dev pod's monitor glows cyan bold while someone works there
  for (const e of state.employees) {
    if (e.sprite !== "working" || !POD_SEAT.test(e.seat)) continue;
    const a = seatAnchor(e.seat); // pod chair; the "[=]" monitor sits 1 right, 2 up
    restyle(g, W, H, a.x + 1, a.y - 2, 3, {color: "cyan", bold: true});
  }

  // speech bubbles above sprites (live ones only, one per employee)
  const byId = new Map(state.employees.map((e) => [e.id, e]));
  const shown = new Set<string>();
  for (const b of (state.bubbles ?? []).filter((bb) => bb.untilTick >= state.tick)) {
    const e = byId.get(b.employeeId);
    if (!e || shown.has(e.id)) continue;
    shown.add(e.id);
    const p = spritePosition(e.id) ?? seatAnchor(e.seat);
    drawBubble(g, W, H, b.text, p.x + 1, p.y);
  }

  return g.map(mergeRow);
}

/** Layout-only view of the SAME rows (tests assert against this; single source of truth). */
export function buildGridPlain(state: OfficeState, width: number, height: number): string[] {
  return buildGridRows(state, width, height).map((row) => row.segments.map((s) => s.text).join(""));
}

/** @deprecated kept for compatibility; delegates to buildGridPlain (one source of truth). */
export function buildGrid(state: OfficeState, width: number, height: number): string[] {
  return buildGridPlain(state, width, height);
}

/**
 * Floor fills the box the shell gives it: exactly `width` cols x `height` rows —
 * grid (height-1 lines, with its own +-+ walls) + the legend line underneath.
 * No extra ink border: the grid IS the frame, so nothing clips.
 */
export function Floor({
  state,
  width = 120,
  height = 28,
}: {
  state: OfficeState;
  width?: number;
  height?: number;
}) {
  const rows = buildGridRows(state, width, Math.max(1, height - 1));
  return (
    <Box flexDirection="column" width={width} flexShrink={0}>
      {rows.map((row, ri) => (
        <Text key={ri}>
          {row.segments.map((seg, si) => (
            <Text key={`${ri}-${si}`} color={seg.color} bold={seg.bold} dimColor={seg.dim}>
              {seg.text}
            </Text>
          ))}
        </Text>
      ))}
      <Text wrap="truncate">
        <Text color="yellow">M=boss </Text>
        <Text color="red">H=hr </Text>
        <Text color="cyan">T=dev </Text>
        <Text color="green">S=scout </Text>
        <Text color="magenta">D=rev </Text>
        <Text color="blue">R=run </Text>
        <Text color="gray">[tea]=break</Text>
      </Text>
    </Box>
  );
}

export default Floor;
