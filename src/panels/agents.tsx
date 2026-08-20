/**
 * agents.tsx — AGENTS roster: the command-center panel.
 * Boss pinned first as "boss (oikonomos)" (yellow bold); one row per employee:
 *   name (role color)  <glyph> <sprite word (semantic color)>  <task, dim, right>
 * Sprite-word colors: working cyan, meeting yellow, coffee yellow-dim,
 * blocked red bold, at desk blackBright (neutral chrome).
 * Blocked (at-mailbox) employees show "blocked" in bold red.
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

/** Semantic chrome color per sprite word (keyed on WORD's display string). */
const WORD_STYLE: Record<string, {color: string; dim?: boolean; bold?: boolean}> = {
  working: {color: "cyan"},
  meeting: {color: "yellow"},
  coffee: {color: "yellow", dim: true},
  blocked: {color: "red", bold: true},
  "at desk": {color: "blackBright"},
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
        const isBoss = e.role === "manager";
        const label = isBoss ? "boss (oikonomos)" : e.name;
        const word = WORD[e.sprite];
        const style = WORD_STYLE[word] ?? {color: "white"};
        const leftLen = label.length + 1 + ROLE_GLYPH[e.role].length + 1 + word.length;
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
              <Text color={nameColor(label)} bold={isBoss}>
                {label}
              </Text>
              <Text> </Text>
              <Text color={nameColor(label)}>{ROLE_GLYPH[e.role]}</Text>
              <Text> </Text>
              <Text color={style.color} dimColor={style.dim} bold={style.bold}>
                {word}
              </Text>
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
