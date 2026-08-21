/**
 * topbar.tsx — one-line app bar, full width:
 *   left:  grafeio v0.1.0 | MODE | agents <n>
 *   right: <office clock> | <cwd basename>
 * Rendered as an inverted (blackBright bg) bar with colored segments:
 * app name bold white, DEMO yellow / LIVE green, agents count cyan,
 * clock + cwd dim (brightBlack fg on a blackBright bar is invisible).
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
  const mode = state.mode.toUpperCase();
  const modeColor = state.mode === "demo" ? "yellow" : "green";
  const agents = String(state.employees.length);
  const clock = officeClock(state.tick);
  const cwd = basename(process.cwd());
  const leftLen = ` grafeio v0.1.0 | ${mode} | agents ${agents}`.length;
  const rightLen = `${clock} | ${cwd} `.length;
  const gap = " ".repeat(Math.max(1, width - leftLen - rightLen));
  return (
    <Text backgroundColor="blackBright" wrap="truncate">
      <Text bold color="white">
        {" grafeio v0.1.0"}
      </Text>
      <Text color="white">{" | "}</Text>
      <Text color={modeColor}>{mode}</Text>
      <Text color="white">{" | agents "}</Text>
      <Text color="cyan">{agents}</Text>
      <Text>{gap}</Text>
      <Text dimColor>{clock}</Text>
      <Text color="white">{" | "}</Text>
      <Text dimColor>{`${cwd} `}</Text>
    </Text>
  );
}
