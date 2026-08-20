/**
 * floor.tsx — the office floor as an ASCII char grid, DYNAMICALLY sized.
 * buildGrid() is PURE: (state, width, height) -> exactly `height` strings
 * of length `width`. Ink only paints them.
 */
import React from "react";
import {Box, Text} from "ink";
import type {OfficeState} from "../state.js";
import {computePlan, tickClock, type Zone} from "./floorplan.js";
import {seatAnchor} from "./roster.js";
import {spriteFrame, spritePosition} from "./sprites.js";

export const LEGEND = "M=boss H=hr T=dev S=scout D=rev R=run [tea]=break";

function put(g: string[][], W: number, H: number, x: number, y: number, s: string): void {
  if (y < 0 || y >= H) return;
  for (let i = 0; i < s.length; i++) {
    const xx = x + i;
    if (xx >= 0 && xx < W) g[y][xx] = s[i];
  }
}

/** Walled room: "+" corners, wall char for the sides, skipped cells at door gaps. */
function drawZone(g: string[][], W: number, H: number, z: Zone): void {
  const wall = z.wall === "-" ? "-" : z.wall;
  const inDoor = (side: "n" | "s" | "e" | "w", i: number): boolean =>
    z.doors.some((d) => d.side === side && i >= d.at && i < d.at + d.size);
  for (let dx = 0; dx < z.w; dx++) {
    if (!inDoor("n", dx)) put(g, W, H, z.x + dx, z.y, dx === 0 || dx === z.w - 1 ? "+" : wall);
    if (!inDoor("s", dx)) put(g, W, H, z.x + dx, z.y + z.h - 1, dx === 0 || dx === z.w - 1 ? "+" : wall);
  }
  for (let dy = 1; dy < z.h - 1; dy++) {
    if (!inDoor("w", dy)) put(g, W, H, z.x, z.y + dy, z.wall === "-" ? "|" : wall);
    if (!inDoor("e", dy)) put(g, W, H, z.x + z.w - 1, z.y + dy, z.wall === "-" ? "|" : wall);
  }
}

const BUBBLE_W = 16; // 14 text cols + "|" borders

function centerPad(t: string, n: number): string {
  const room = Math.max(0, n - t.length);
  const l = Math.floor(room / 2);
  return " ".repeat(l) + t + " ".repeat(room - l);
}

/**
 * ".--------------." / "|   big day.   |" / "+--*-----------+" three rows
 * directly above the sprite. Clipped at the grid top (colliding rows drop).
 */
function drawBubble(g: string[][], W: number, H: number, text: string, cx: number, cy: number): void {
  const t = centerPad(text.slice(0, 14), 14);
  const x = Math.max(0, Math.min(Math.max(0, W - BUBBLE_W), cx - Math.floor(BUBBLE_W / 2)));
  const lines = [
    "." + "-".repeat(14) + ".",
    "|" + t + "|",
    "+" + "--*" + "-".repeat(11) + "+",
  ];
  for (let i = 0; i < lines.length; i++) {
    const ry = cy - 3 + i;
    if (ry >= 1) put(g, W, H, x, ry, lines[i]);
  }
}

export function buildGrid(state: OfficeState, width: number, height: number): string[] {
  const W = Math.max(1, Math.floor(width));
  const H = Math.max(1, Math.floor(height));
  const plan = computePlan(width, height);
  const g = Array.from({length: H}, () => new Array<string>(W).fill(" "));

  // outer border
  if (W >= 2 && H >= 2) {
    put(g, W, H, 0, 0, "+" + "-".repeat(W - 2) + "+");
    put(g, W, H, 0, H - 1, "+" + "-".repeat(W - 2) + "+");
    for (let y = 1; y < H - 1; y++) {
      put(g, W, H, 0, y, "|");
      put(g, W, H, W - 1, y, "|");
    }
  } else {
    for (let y = 0; y < H; y++) put(g, W, H, 0, y, "-".repeat(W));
  }

  // furniture first, walls over any spill, clock on the boss-office wall
  for (const p of plan.props) put(g, W, H, p.x, p.y, p.glyph);
  for (const z of plan.zones) drawZone(g, W, H, z);
  put(g, W, H, plan.hotspots.clock.x, plan.hotspots.clock.y, tickClock(state.tick));

  // degrade badge (drawn once, centered)
  if (plan.tiny) {
    const badge = "small terminal";
    put(g, W, H, Math.max(0, Math.floor((W - badge.length) / 2)), Math.floor(H / 2), badge);
  }

  // animated sprites, stamped over the floor
  for (const e of state.employees) {
    const p = spritePosition(e.id) ?? seatAnchor(e.seat);
    put(g, W, H, p.x, p.y, spriteFrame(e.role, e.sprite, state.tick));
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

  return g.map((row) => row.join(""));
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
  const lines = buildGrid(state, width, Math.max(1, height - 1));
  return (
    <Box flexDirection="column" width={width} flexShrink={0}>
      <Text>{lines.join("\n")}</Text>
      <Text wrap="truncate">
        <Text color="yellow">M=boss </Text>
        <Text color="red">H=hr </Text>
        <Text color="cyan">T=dev </Text>
        <Text color="green">S=scout </Text>
        <Text color="magenta">D=rev </Text>
        <Text color="blue">R=run </Text>
        <Text color="brightBlack">[tea]=break</Text>
      </Text>
    </Box>
  );
}

export default Floor;
