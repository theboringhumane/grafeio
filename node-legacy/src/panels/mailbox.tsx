/**
 * mailbox.tsx — MAIL: latest 10 mails, newest first.
 * Sized for the 36-col sidebar: flex-grows to share space with the board,
 * clips cleanly when the panel gets short.
 * Colors: kind letter B=cyan R=green N=blackBright U=white;
 * from-name in the sender's role color; ">to subject" stays default.
 */
import React from "react";
import {Box, Text} from "ink";
import type {MailItem} from "../state.js";
import {nameColor} from "../office/roster.js";

const KIND_LETTER: Record<MailItem["kind"], string> = {
  brief: "B",
  return: "R",
  notice: "N",
  user: "U",
};

const KIND_COLOR: Record<MailItem["kind"], string> = {
  brief: "cyan",
  return: "green",
  notice: "blackBright",
  user: "white",
};

export function Mailbox({mails}: {mails: MailItem[]}) {
  const rows = [...mails].sort((a, b) => b.at - a.at).slice(0, 10);
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
      <Text bold>MAIL</Text>
      {rows.length === 0 ? <Text dimColor>- empty -</Text> : null}
      {rows.map((m) => (
        <Text key={m.id} wrap="truncate">
          <Text>[</Text>
          <Text color={KIND_COLOR[m.kind]}>{KIND_LETTER[m.kind]}</Text>
          <Text>] </Text>
          <Text color={nameColor(m.from)}>{m.from}</Text>
          <Text>{`>${m.to} ${m.subject}`}</Text>
        </Text>
      ))}
    </Box>
  );
}
