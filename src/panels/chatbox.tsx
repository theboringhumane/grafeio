/**
 * chatbox.tsx — persistent bottom bar, exactly CHAT_H rows, full width:
 *   ╭─ CHAT -> BOSS ─────╮   (title lives in the top border)
 *   │ <last 3 messages>   │
 *   │ > <input>           │
 *   ╰─────────────────────╯
 * While a boss reply is pending, the input locks (frozen value, no focus)
 * and a typing indicator cycles. Manual borders keep the height exact.
 */
import React, {useState} from "react";
import {Box, Text} from "ink";
import TextInput from "ink-text-input";
import type {ChatMsg} from "../state.js";

export const CHAT_H = 6;

interface Line {
  key: string;
  text: string;
  color: string;
  dim: boolean;
}

export function ChatBox({
  chat,
  tick,
  width,
  onSend,
}: {
  chat: ChatMsg[];
  tick: number;
  width: number;
  onSend: (text: string) => void;
}) {
  const [value, setValue] = useState("");
  const pending = chat.some((m) => m.from === "boss" && m.pending);
  const dots = ".".repeat((tick % 3) + 1);

  const w = Math.max(12, width);
  const contentW = w - 4; // inside "| " + " |"

  const lines: Line[] = chat.slice(-3).map((m) =>
    m.from === "user"
      ? {key: m.id, text: `you> ${m.text}`, color: "white", dim: false}
      : m.pending
        ? {key: m.id, text: `boss> typing${dots}`, color: "yellow", dim: true}
        : {key: m.id, text: `boss> ${m.text}`, color: "yellow", dim: false},
  );
  while (lines.length < 3)
    lines.push({key: `pad-${lines.length}`, text: "", color: "white", dim: false});

  const title = "CHAT -> BOSS";
  const topBody = `╭─ ${title} ${"─".repeat(w)}`;
  const top = `${topBody.slice(0, Math.max(0, w - 1))}╮`;
  const bottom = `╰${"─".repeat(Math.max(0, w - 2))}╯`;

  const inputMax = Math.max(4, contentW - 2); // "> " prefix eats 2

  const submit = (v: string) => {
    if (pending) return;
    if (!v.trim()) return;
    onSend(v);
    setValue("");
  };

  return (
    <Box flexDirection="column" width={w} height={CHAT_H}>
      <Text color="yellow" wrap="truncate">
        {top}
      </Text>
      {lines.map((l) => {
        const shown = l.text.slice(0, Math.max(0, contentW));
        return (
          <Text key={l.key} wrap="truncate">
            <Text color="yellow">{"│ "}</Text>
            <Text color={l.color} dimColor={l.dim}>
              {shown}
              {" ".repeat(Math.max(0, contentW - shown.length))}
            </Text>
            <Text color="yellow">{" │"}</Text>
          </Text>
        );
      })}
      <Box flexDirection="row" width={w} height={1}>
        <Text color="yellow">{"│ "}</Text>
        <Box flexDirection="row" flexGrow={1} overflow="hidden">
          <Text>{"> "}</Text>
          <TextInput
            value={value}
            focus={!pending}
            placeholder={pending ? "boss is typing..." : "talk to the boss"}
            onChange={(v) => {
              if (pending) return;
              if (v.length <= inputMax) setValue(v);
            }}
            onSubmit={submit}
          />
        </Box>
        <Text color="yellow">{" │"}</Text>
      </Box>
      <Text color="yellow" wrap="truncate">
        {bottom}
      </Text>
    </Box>
  );
}
