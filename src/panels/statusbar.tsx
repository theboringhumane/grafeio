/**
 * statusbar.tsx — one-line status bar, full width:
 *   left:  state.statusLine
 *   right: <n> agents | board <pending>/<doing>/<done> | <mode>
 * Rendered as an inverted (brightBlack bg) bar.
 */
import React from "react";
import {Text} from "ink";
import type {OfficeState} from "../state.js";
import {barLine} from "./topbar.js";

export function StatusBar({state, width}: {state: OfficeState; width: number}) {
  const p = state.tasks.filter((t) => t.status === "pending").length;
  const i = state.tasks.filter((t) => t.status === "in-progress").length;
  const d = state.tasks.filter((t) => t.status === "done").length;
  const left = ` ${state.statusLine}`;
  const right = `${state.employees.length} agents | board ${p}/${i}/${d} | ${state.mode} `;
  return (
    <Text backgroundColor="brightBlack" color="white" wrap="truncate">
      {barLine(left, right, width)}
    </Text>
  );
}
