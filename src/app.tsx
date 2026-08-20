/**
 * app.tsx — Ink layout grid + the office reducer.
 * UI never calls SDK/HTTP directly; everything lives off OfficeBackend events.
 */
import React, {useEffect, useReducer} from "react";
import {Box, Text} from "ink";
import type {
  BoardTask,
  OfficeBackend,
  OfficeEvent,
  OfficeState,
} from "./state.js";
import {assignSeat} from "./office/roster.js";
import {advanceSprites} from "./office/sprites.js";
import {Floor} from "./office/floor.js";
import {Mailbox} from "./panels/mailbox.js";
import {Taskboard} from "./panels/taskboard.js";
import {ChatBox} from "./panels/chatbox.js";

const MAIL_CAP = 30;
const CHAT_CAP = 30;

export function initialState(mode: "live" | "demo"): OfficeState {
  return {
    employees: [
      {id: "manager", name: "boss", role: "manager", seat: "manager", sprite: "at-desk"},
      {id: "hr", name: "hr", role: "hr", seat: "hr", sprite: "at-desk"},
    ],
    tasks: [],
    mails: [],
    chat: [],
    mode,
    statusLine: `[grafeio] ${mode} — booting...`,
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

export function officeReducer(state: OfficeState, event: OfficeEvent): OfficeState {
  switch (event.type) {
    case "tick":
      return advanceSprites({...state, tick: state.tick + 1});

    case "hire": {
      if (state.employees.some((e) => e.id === event.employee.id)) return state;
      const seat = assignSeat(
        state.employees.map((e) => e.seat),
        event.employee.role,
      );
      return {...state, employees: [...state.employees, {...event.employee, seat}]};
    }

    case "fire":
      return {...state, employees: state.employees.filter((e) => e.id !== event.employeeId)};

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

    case "status":
      return {...state, statusLine: event.text};

    default:
      return state;
  }
}

export function App({backend}: {backend: OfficeBackend}) {
  const [state, dispatch] = useReducer(officeReducer, backend.mode, initialState);

  useEffect(() => {
    backend.start((emit) => dispatch(emit));
    const t = setInterval(() => dispatch({type: "tick"}), 180);
    return () => {
      clearInterval(t);
      backend.stop();
    };
  }, [backend]);

  return (
    <Box flexDirection="column">
      <Box flexDirection="row">
        <Floor state={state} />
        <Box flexDirection="column" width={30}>
          <Mailbox mails={state.mails} />
          <Taskboard tasks={state.tasks} />
        </Box>
      </Box>
      <Text color="brightBlack" wrap="truncate">
        {state.statusLine}
      </Text>
      <ChatBox chat={state.chat} tick={state.tick} onSend={(text) => backend.send(text)} />
    </Box>
  );
}
