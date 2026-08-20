/**
 * topbar.tsx — one-line app bar, full width:
 *   left:  grafeio v0.1.0 | MODE | agents <n>
 *   right: <office clock> | <cwd basename>
 * Rendered as an inverted (brightBlack bg) bar.
 */
import React from "react";
import {Text} from "ink";
import {basename} from "node:path";
import type {OfficeState} from "../state.js";

/** Office clock: starts 09:00, +1 minute per ~30 ticks. */
export function officeClock(tick: number): string {
  const minutes = Math.floor(Math.max(0, tick) / 30) % (12 * 60);
  const hh = String(9 + Math.floor(minutes / 60)).padStart(2, "0");
  const mm = String(minutes % 60).padStart(2, "0");
  return `${hh}:${mm}`;
}

/** Shared one-line bar: left and right strings pinned to the edges. */
export function barLine(left: string, right: string, width: number): string {
  const gap = Math.max(1, width - left.length - right.length);
  return (left + " ".repeat(gap) + right).slice(0, Math.max(0, width));
}

export function TopBar({state, width}: {state: OfficeState; width: number}) {
  const left = ` grafeio v0.1.0 | ${state.mode.toUpperCase()} | agents ${state.employees.length}`;
  const right = `${officeClock(state.tick)} | ${basename(process.cwd())} `;
  return (
    <Text backgroundColor="brightBlack" color="white" wrap="truncate">
      {barLine(left, right, width)}
    </Text>
  );
}
