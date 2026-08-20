/**
 * app.tsx — opencode-style chrome: fixed to the complete terminal.
 *
 * Vertical stack, full width = measured cols:
 *   TOPBAR (1) | MIDDLE (floor + 36-col sidebar) | CHATBOX (6) | STATUSBAR (1)
 *
 * UI never calls SDK/HTTP directly; everything lives off OfficeBackend events.
 * The Floor component (src/office/floor.tsx) is owned elsewhere and is fed
 * measured {state, width, height} per the floor contract.
 */
import React, {useEffect, useReducer, useState} from "react";
import {Box, useStdout} from "ink";
import type {
  BoardTask,
  OfficeBackend,
  OfficeEvent,
  OfficeState,
  SpeechBubble,
} from "./state.js";
import {assignSeat} from "./office/roster.js";
import {advanceSprites} from "./office/sprites.js";
import {Floor} from "./office/floor.js";
import {Agents} from "./panels/agents.js";
import {Mailbox} from "./panels/mailbox.js";
import {Taskboard} from "./panels/taskboard.js";
import {ChatBox, CHAT_H} from "./panels/chatbox.js";
import {TopBar} from "./panels/topbar.js";
import {StatusBar} from "./panels/statusbar.js";

const MAIL_CAP = 30;
const CHAT_CAP = 30;
const BUBBLE_CAP = 3; // never more than 3 concurrent balloons (drop oldest)
const SIDEBAR_W = 36;
const TICK_MS = 180;
const AMBIENT_EVERY = 140; // ticks between ambient bubbles

// pure ASCII — members of the floor typed these, not a program
const AMBIENT_LINES = [
  "big day. lots of meetings.",
  "shipping friday.",
  "who took the red mug?",
  "standup in 5.",
  "this diff is a crime scene.",
  "coffee machine is empty again.",
  "review queue is deep today.",
  "anyone seen the staging key?",
];

export function initialState(mode: "live" | "demo"): OfficeState {
  return {
    employees: [
      {id: "manager", name: "boss", role: "manager", seat: "manager", sprite: "at-desk"},
      {id: "hr", name: "hr", role: "hr", seat: "hr", sprite: "at-desk"},
    ],
    tasks: [],
    mails: [],
    chat: [],
    bubbles: [],
    mode,
    statusLine: `[grafeio] ${mode} - booting...`,
    tick: 0,
  };
}

function cap<T>(list: T[], max: number): T[] {
  return list.length > max ? list.slice(list.length - max) : list;
}

function upsertTask(tasks: BoardTask[], task: BoardTask): BoardTask[] {
  const i = tasks.findIndex((t) => t.id === task.id);
  if (i === -1) return [...tasks, task];
  const next = tasks.slice();
  next[i] = task;
  return next;
}

const pick = <T,>(list: T[]): T => list[Math.floor(Math.random() * list.length)];

export function officeReducer(state: OfficeState, event: OfficeEvent): OfficeState {
  switch (event.type) {
    case "tick": {
      const tick = state.tick + 1;
      // drop expired balloons
      const bubbles = state.bubbles.filter((b) => b.untilTick > tick);
      let next = advanceSprites({...state, tick, bubbles});

      // ambient chatter: every ~140 ticks a random working non-manager speaks
      if (tick % AMBIENT_EVERY === 0) {
        const working = next.employees.filter(
          (e) => e.role !== "manager" && e.sprite === "working",
        );
        if (working.length > 0) {
          next = officeReducer(next, {
            type: "bubble",
            employeeId: pick(working).id,
            text: pick(AMBIENT_LINES),
          });
        } else if (tick % (AMBIENT_EVERY * 2) === 0) {
          // nobody working: occasionally an idle one breaks the silence
          const idle = next.employees.filter(
            (e) => e.role !== "manager" && e.sprite === "at-desk",
          );
          if (idle.length > 0) {
            next = officeReducer(next, {
              type: "bubble",
              employeeId: pick(idle).id,
              text: "quiet floor today.",
            });
          }
        }
      }
      return next;
    }

    case "hire": {
      if (state.employees.some((e) => e.id === event.employee.id)) return state;
      const seat = assignSeat(
        state.employees.map((e) => e.seat),
        event.employee.role,
      );
      return {...state, employees: [...state.employees, {...event.employee, seat}]};
    }

    case "fire":
      return {
        ...state,
        employees: state.employees.filter((e) => e.id !== event.employeeId),
        // their balloons pop with them
        bubbles: state.bubbles.filter((b) => b.employeeId !== event.employeeId),
      };

    case "dispatch": {
      const owner = state.employees.find((e) => e.id === event.employeeId);
      const task: BoardTask = {
        ...event.task,
        status: "in-progress",
        owner: owner?.name ?? event.task.owner,
      };
      return {
        ...state,
        tasks: upsertTask(state.tasks, task),
        employees: state.employees.map((e) =>
          e.id === event.employeeId ? {...e, sprite: "to-manager" as const, task: task.title} : e,
        ),
      };
    }

    case "working": {
      const owner = state.employees.find((e) => e.id === event.employeeId);
      return {
        ...state,
        employees: state.employees.map((e) =>
          e.id === event.employeeId ? {...e, sprite: "working" as const} : e,
        ),
        tasks: event.taskId
          ? state.tasks.map((t) =>
              t.id === event.taskId
                ? {...t, status: "in-progress" as const, owner: t.owner ?? owner?.name}
                : t,
            )
          : state.tasks,
      };
    }

    case "returned":
      return {
        ...state,
        employees: state.employees.map((e) =>
          e.id === event.employeeId ? {...e, sprite: "to-desk" as const, task: undefined} : e,
        ),
        tasks: state.tasks.map((t) =>
          t.id === event.taskId ? {...t, status: "done" as const} : t,
        ),
        mails: cap([...state.mails, event.mail], MAIL_CAP),
      };

    case "idle-drift":
      return {
        ...state,
        employees: state.employees.map((e) =>
          e.id === event.employeeId ? {...e, sprite: "to-coffee" as const} : e,
        ),
      };

    case "blocked":
      return {
        ...state,
        employees: state.employees.map((e) =>
          e.id === event.employeeId ? {...e, sprite: "at-mailbox" as const} : e,
        ),
        statusLine: `[blocked] ${event.note}`,
      };

    case "task":
      return {...state, tasks: upsertTask(state.tasks, event.task)};

    case "mail":
      return {...state, mails: cap([...state.mails, event.mail], MAIL_CAP)};

    case "chat-user":
      return {...state, chat: cap([...state.chat, event.msg], CHAT_CAP)};

    case "chat-boss": {
      // a real answer (or a fresh pending) replaces the old typing placeholder
      const rest = state.chat.filter((m) => !(m.from === "boss" && m.pending));
      return {...state, chat: cap([...rest, event.msg], CHAT_CAP)};
    }

    case "bubble": {
      const bubble: SpeechBubble = {
        id: `bbl-${state.tick}-${Math.random().toString(36).slice(2, 7)}`,
        employeeId: event.employeeId,
        text: event.text,
        untilTick: state.tick + (event.ttl ?? 40),
      };
      return {...state, bubbles: cap([...state.bubbles, bubble], BUBBLE_CAP)};
    }

    case "status":
      return {...state, statusLine: event.text};

    default:
      return state;
  }
}

export interface AppProps {
  backend: OfficeBackend;
  /** fixed geometry for snapshot harnesses; defaults to live stdout */
  cols?: number;
  rows?: number;
}

export function App({backend, cols, rows}: AppProps) {
  const [state, dispatch] = useReducer(officeReducer, backend.mode, initialState);
  const {stdout} = useStdout();
  const fixed = cols !== undefined && rows !== undefined;
  const [dims, setDims] = useState({
    cols: cols ?? stdout?.columns ?? 100,
    rows: rows ?? stdout?.rows ?? 30,
  });

  // dynamic: follow the terminal on every resize
  useEffect(() => {
    if (fixed) return;
    const onResize = () =>
      setDims({cols: stdout?.columns ?? 100, rows: stdout?.rows ?? 30});
    stdout?.on("resize", onResize);
    return () => {
      stdout?.off("resize", onResize);
    };
  }, [fixed, stdout]);

  // fullscreen: borrow the alternate screen while we are a real TTY
  useEffect(() => {
    const isTty = Boolean((stdout as unknown as {isTTY?: boolean} | undefined)?.isTTY);
    if (fixed || !isTty || !stdout) return;
    stdout.write("\x1b[?1049h");
    const restore = () => stdout.write("\x1b[?1049l");
    process.on("SIGINT", restore);
    process.on("SIGTERM", restore);
    return () => {
      process.off("SIGINT", restore);
      process.off("SIGTERM", restore);
      restore();
    };
  }, [fixed, stdout]);

  useEffect(() => {
    void backend.start((emit) => dispatch(emit));
    const t = setInterval(() => dispatch({type: "tick"}), TICK_MS);
    return () => {
      clearInterval(t);
      void backend.stop();
    };
  }, [backend]);

  const totalCols = Math.max(40, dims.cols);
  const totalRows = Math.max(12, dims.rows);
  const middleH = Math.max(8, totalRows - 1 - CHAT_H - 1);
  const floorW = Math.max(8, totalCols - SIDEBAR_W);

  return (
    <Box flexDirection="column" width={totalCols} height={totalRows}>
      <TopBar state={state} width={totalCols} />
      <Box flexDirection="row" width={totalCols} height={middleH}>
        <Box width={floorW} height={middleH} overflow="hidden">
          <Floor state={state} width={floorW} height={middleH} />
        </Box>
        <Box flexDirection="column" width={SIDEBAR_W} height={middleH} overflow="hidden">
          <Agents state={state} />
          <Mailbox mails={state.mails} />
          <Taskboard tasks={state.tasks} />
        </Box>
      </Box>
      <ChatBox chat={state.chat} tick={state.tick} width={totalCols} onSend={(text) => void backend.send(text)} />
      <StatusBar state={state} width={totalCols} />
    </Box>
  );
}
