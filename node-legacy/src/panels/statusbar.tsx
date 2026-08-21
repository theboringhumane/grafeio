/**
 * statusbar.tsx — one-line status bar, full width:
 *   left:  state.statusLine (heuristic color: blocked/failed/offline -> red,
 *          live -> green, demo -> yellow, else dim neutral)
 *   right: <n> agents (cyan) | board <p>/<i>/<d> (yellow/cyan/green) | <mode> (yellow|green)
 * Rendered as an inverted (blackBright bg) bar.
 */
import React from "react";
import {Text} from "ink";
import type {OfficeState} from "../state.js";

/** Neutral/attention color for the free-text status line. */
function statusLineStyle(line: string): {color?: string; dim?: boolean} {
  const s = line.toLowerCase();
  if (s.includes("blocked") || s.includes("failed") || s.includes("offline")) return {color: "red"};
  if (s.includes("live")) return {color: "green"};
  if (s.includes("demo")) return {color: "yellow"};
  return {dim: true}; // neutral chrome; brightBlack fg would vanish on the bar bg
}

export function StatusBar({state, width}: {state: OfficeState; width: number}) {
  const p = state.tasks.filter((t) => t.status === "pending").length;
  const i = state.tasks.filter((t) => t.status === "in-progress").length;
  const d = state.tasks.filter((t) => t.status === "done").length;
  const agents = String(state.employees.length);
  const style = statusLineStyle(state.statusLine);
  const modeColor = state.mode === "demo" ? "yellow" : "green";
  const leftLen = ` ${state.statusLine}`.length;
  const rightLen = `${agents} agents | board ${p}/${i}/${d} | ${state.mode} `.length;
  const gap = " ".repeat(Math.max(1, width - leftLen - rightLen));
  return (
    <Text backgroundColor="blackBright" wrap="truncate">
      <Text color={style.color} dimColor={style.dim}>{` ${state.statusLine}`}</Text>
      <Text>{gap}</Text>
      <Text color="cyan">{agents}</Text>
      <Text color="white">{" agents | board "}</Text>
      <Text color="yellow">{p}</Text>
      <Text color="white">{"/"}</Text>
      <Text color="cyan">{i}</Text>
      <Text color="white">{"/"}</Text>
      <Text color="green">{d}</Text>
      <Text color="white">{" | "}</Text>
      <Text color={modeColor}>{state.mode}</Text>
      <Text color="white">{" "}</Text>
    </Text>
  );
}
