/**
 * taskboard.tsx — BOARD: three columns PENDING | DOING | DONE.
 * Sized for the 36-col sidebar: tight columns, truncated rows,
 * flex-grows to share space with the mailbox, clips cleanly when short.
 */
import React from "react";
import {Box, Text} from "ink";
import type {BoardTask, TaskStatus} from "../state.js";
import {nameColor} from "../office/roster.js";

const CAP = 6;

const COLS: {title: string; status: TaskStatus}[] = [
  {title: "PENDING", status: "pending"},
  {title: "DOING", status: "in-progress"},
  {title: "DONE", status: "done"},
];

function Column({title, status, tasks}: {title: string; status: TaskStatus; tasks: BoardTask[]}) {
  const rows = tasks.filter((t) => t.status === status).sort((a, b) => a.at - b.at);
  const shown = rows.slice(0, CAP);
  const overflow = rows.length - shown.length;
  const done = status === "done";
  return (
    <Box flexDirection="column" flexGrow={1} flexBasis={0} overflow="hidden">
      <Text bold underline wrap="truncate">
        {title}
      </Text>
      {shown.length === 0 ? <Text dimColor>-</Text> : null}
      {shown.map((t) => (
        <Text
          key={t.id}
          wrap="truncate"
          dimColor={done}
          color={done ? undefined : nameColor(t.owner ?? "")}
        >
          {`${t.title}${t.owner ? ` ${t.owner}` : ""}`}
        </Text>
      ))}
      {overflow > 0 ? <Text dimColor>{`+${overflow} more`}</Text> : null}
    </Box>
  );
}

export function Taskboard({tasks}: {tasks: BoardTask[]}) {
  return (
    <Box
      borderStyle="round"
      borderColor="white"
      flexDirection="column"
      paddingX={1}
      flexGrow={1}
      flexBasis={0}
      overflow="hidden"
    >
      <Text bold>BOARD</Text>
      <Box flexDirection="row" columnGap={1} flexGrow={1} overflow="hidden">
        {COLS.map((c) => (
          <Column key={c.status} title={c.title} status={c.status} tasks={tasks} />
        ))}
      </Box>
    </Box>
  );
}
