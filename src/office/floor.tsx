/**
 * floor.tsx — the office floor as a fixed ASCII char grid.
 * buildGrid() is PURE: it returns strings, Ink only paints them.
 */
import React from "react";
import {Box, Text} from "ink";
import type {OfficeState} from "../state.js";
import {SEATS, seatAnchor} from "./roster.js";
import {spriteFrame, spritePosition} from "./sprites.js";

export const FLOOR_W = 66;
export const FLOOR_H = 20;

function put(grid: string[][], x: number, y: number, s: string): void {
  if (y < 0 || y >= FLOOR_H) return;
  for (let i = 0; i < s.length; i++) {
    const xx = x + i;
    if (xx >= 0 && xx < FLOOR_W) grid[y][xx] = s[i];
  }
}

/** Wall-clock that runs on office time: 09:00 + 1 minute per tick. */
export function clockText(tick: number): string {
  const m = Math.max(0, tick) % 720;
  const hh = 9 + Math.floor(m / 60);
  const mm = m % 60;
  return `[${String(hh).padStart(2, "0")}:${String(mm).padStart(2, "0")}]`;
}

export function buildGrid(state: OfficeState): string[] {
  const g = Array.from({length: FLOOR_H}, () => new Array<string>(FLOOR_W).fill(" "));

  // walls
  for (let x = 0; x < FLOOR_W; x++) {
    g[0][x] = "-";
    g[FLOOR_H - 1][x] = "-";
  }
  for (let y = 0; y < FLOOR_H; y++) {
    g[y][0] = "|";
    g[y][FLOOR_W - 1] = "|";
  }
  g[0][0] = "+";
  g[0][FLOOR_W - 1] = "+";
  g[FLOOR_H - 1][0] = "+";
  g[FLOOR_H - 1][FLOOR_W - 1] = "+";

  // static art: clock, tea machine, mail tray, water cooler, plant
  put(g, 57, 0, clockText(state.tick));
  put(g, 58, 1, "[tea]");
  put(g, 40, 2, "[mail]");
  put(g, 2, 16, "[h2o]");
  put(g, 61, 18, "(Y)");

  // furniture (always), name labels (only for occupied seats)
  for (const def of Object.values(SEATS)) {
    if (def.desk) put(g, def.desk.x, def.desk.y, def.desk.glyph);
  }
  for (const e of state.employees) {
    const def = SEATS[e.seat];
    if (def?.label) put(g, def.label.x, def.label.y, e.name.slice(0, 8));
  }

  // animated sprites, stamped last so they walk over the floor
  for (const e of state.employees) {
    const p = spritePosition(e.id) ?? seatAnchor(e.seat);
    put(g, p.x, p.y, spriteFrame(e.role, e.sprite, state.tick));
  }
  return g.map((row) => row.join(""));
}

export function Floor({state}: {state: OfficeState}) {
  const lines = buildGrid(state);
  return (
    <Box borderStyle="round" borderColor="white" flexDirection="column" flexGrow={1} paddingX={1}>
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
