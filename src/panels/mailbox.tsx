/**
 * mailbox.tsx — MAIL: latest 10 mails, newest first.
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

export function Mailbox({mails}: {mails: MailItem[]}) {
  const rows = [...mails].sort((a, b) => b.at - a.at).slice(0, 10);
  return (
    <Box borderStyle="round" borderColor="white" flexDirection="column" paddingX={1}>
      <Text bold>MAIL</Text>
      {rows.length === 0 ? <Text dimColor>- empty -</Text> : null}
      {rows.map((m) => (
        <Text key={m.id} wrap="truncate" color={nameColor(m.from)}>
          {`[${KIND_LETTER[m.kind]}] ${m.from}>${m.to} ${m.subject}`}
        </Text>
      ))}
    </Box>
  );
}
