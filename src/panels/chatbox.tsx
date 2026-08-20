/**
 * chatbox.tsx — CHAT -> BOSS: last 6 messages above a prompt to the boss.
 * While a boss reply is pending, the input is locked and a typing indicator cycles.
 */
import React, {useState} from "react";
import {Box, Text} from "ink";
import TextInput from "ink-text-input";
import type {ChatMsg} from "../state.js";

export function ChatBox({
  chat,
  tick,
  onSend,
}: {
  chat: ChatMsg[];
  tick: number;
  onSend: (text: string) => void;
}) {
  const [value, setValue] = useState("");
  const pending = chat.some((m) => m.from === "boss" && m.pending);
  const dots = ".".repeat((tick % 3) + 1);
  const last = chat.slice(-6); // oldest first, newest just above the input

  const submit = (v: string) => {
    if (pending) return;
    if (!v.trim()) return;
    onSend(v);
    setValue("");
  };

  return (
    <Box borderStyle="round" borderColor="yellow" flexDirection="column" paddingX={1}>
      <Text bold color="yellow">
        {"CHAT -> BOSS"}
      </Text>
      {last.map((m) =>
        m.from === "user" ? (
          <Text key={m.id} color="white" wrap="truncate">
            {`you> ${m.text}`}
          </Text>
        ) : m.pending ? (
          <Text key={m.id} color="yellow" dimColor wrap="truncate">
            {`boss> typing${dots}`}
          </Text>
        ) : (
          <Text key={m.id} color="yellow" wrap="truncate">
            {`boss> ${m.text}`}
          </Text>
        ),
      )}
      <Box flexDirection="row">
        <Text>{"> "}</Text>
        <TextInput
          value={value}
          focus={!pending}
          placeholder={pending ? "boss is typing..." : "talk to the boss"}
          onChange={pending ? () => {} : setValue}
          onSubmit={submit}
        />
      </Box>
    </Box>
  );
}
