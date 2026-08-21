/**
 * chatbox.tsx — persistent bottom bar, exactly CHAT_H rows, full width:
 *   ╭─ CHAT -> BOSS ─────╮   (title lives in the top border, blackBright)
 *   │ <last 3 messages>   │   ("you>" cyan, "boss>" yellow, bodies default)
 *   │ > <input>           │
 *   ╰─────────────────────╯
 * While a boss reply is pending, the input locks (frozen value, no focus)
 * and a yellow-dim typing indicator cycles. Manual borders (yellow) keep
 * the height exact.
 */
import React, {useState} from "react";
import {Box, Text} from "ink";
import TextInput from "ink-text-input";
import type {ChatMsg} from "../state.js";

export const CHAT_H = 6;

interface Line {
  key: string;
  /** "you> " / "boss> " — colored; text = message body — default fg. */
  prefix: string;
  prefixColor: string;
  text: string;
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
      ? {key: m.id, prefix: "you> ", prefixColor: "cyan", text: m.text, dim: false}
      : m.pending
        ? {key: m.id, prefix: "boss> ", prefixColor: "yellow", text: `typing${dots}`, dim: true}
        : {key: m.id, prefix: "boss> ", prefixColor: "yellow", text: m.text, dim: false},
  );
  while (lines.length < 3)
    lines.push({key: `pad-${lines.length}`, prefix: "", prefixColor: "white", text: "", dim: false});

  const title = "CHAT -> BOSS";
  // "╭─ " + title + " " + dashes + "╮" == w (matches the previous one-piece layout)
  const dashes = "─".repeat(Math.max(1, w - 1 - 3 - title.length - 1));
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
      <Text wrap="truncate">
        <Text color="yellow">{"╭─ "}</Text>
        <Text color="blackBright">{title}</Text>
        <Text color="yellow">{` ${dashes}╮`}</Text>
      </Text>
      {lines.map((l) => {
        // truncate the composed line first, then split prefix/body for coloring
        const shown = `${l.prefix}${l.text}`.slice(0, Math.max(0, contentW));
        const shownPrefix = shown.slice(0, l.prefix.length);
        const shownText = shown.slice(l.prefix.length);
        const pad = " ".repeat(Math.max(0, contentW - shown.length));
        return (
          <Text key={l.key} wrap="truncate">
            <Text color="yellow">{"│ "}</Text>
            <Text color={l.prefixColor} dimColor={l.dim}>
              {shownPrefix}
            </Text>
            <Text dimColor={l.dim}>{shownText}</Text>
            <Text>{pad}</Text>
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
      <Text wrap="truncate">
        <Text color="yellow">{bottom}</Text>
      </Text>
    </Box>
  );
}
