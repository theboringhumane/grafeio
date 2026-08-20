/**
 * agents.tsx — AGENTS roster: the command-center panel.
 * Boss pinned first as "boss (oikonomos)"; one row per employee:
 *   name (left)  <glyph> <sprite word>  <current task, right-aligned, truncated>
 * Blocked employees show "!blocked" in red.
 */
import React from "react";
import {Box, Text} from "ink";
import type {OfficeState, SpriteState} from "../state.js";
import {ROLE_GLYPH} from "../office/sprites.js";
import {nameColor} from "../office/roster.js";

const WORD: Record<SpriteState, string> = {
  "at-desk": "at desk",
  working: "working",
  "to-manager": "meeting",
  meeting: "meeting",
  "to-desk": "at desk",
  "to-coffee": "coffee",
  coffee: "coffee",
  "at-mailbox": "blocked",
};

const MAX_ROWS = 9;

export function Agents({state}: {state: OfficeState}) {
  const boss = state.employees.find((e) => e.role === "manager");
  const rest = state.employees.filter((e) => e.role !== "manager");
  const ordered = boss ? [boss, ...rest] : rest;
  const shown = ordered.slice(0, MAX_ROWS);
  const overflow = ordered.length - shown.length;

  return (
    <Box borderStyle="round" borderColor="white" flexDirection="column" paddingX={1}>
      <Text bold>AGENTS</Text>
      {shown.map((e) => {
        const blocked = e.sprite === "at-mailbox";
        const label = e.role === "manager" ? "boss (oikonomos)" : e.name;
        const leftLen = label.length + 1 + ROLE_GLYPH[e.role].length + 1 + (blocked ? 8 : WORD[e.sprite].length);
        // 32 = 36-wide sidebar - 2 borders - 2 padding; 1 = separator gap
        const room = 32 - leftLen - 1;
        const task = e.task
          ? e.task.length > room
            ? `${e.task.slice(0, Math.max(0, room - 3))}...`
            : e.task
          : "";
        return (
          <Box key={e.id} flexDirection="row" justifyContent="space-between">
            <Text wrap="truncate">
              <Text>{label}</Text>
              <Text> </Text>
              <Text color={nameColor(label)}>{ROLE_GLYPH[e.role]}</Text>
              <Text> </Text>
              {blocked ? (
                <Text color="red" bold>
                  !blocked
                </Text>
              ) : (
                <Text dimColor>{WORD[e.sprite]}</Text>
              )}
            </Text>
            {task ? (
              <Text dimColor wrap="truncate">
                {task}
              </Text>
            ) : null}
          </Box>
        );
      })}
      {overflow > 0 ? <Text dimColor>{`+${overflow} more`}</Text> : null}
    </Box>
  );
}
