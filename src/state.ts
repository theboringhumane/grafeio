/**
 * OfficeState — the ONE shape backend and UI both speak.
 * UI never calls SDK/HTTP directly. Backend never renders.
 */

export type SpriteState =
  | "at-desk" // idle at own desk
  | "working" // typing at desk
  | "to-manager" // walking to the manager's desk with a task
  | "meeting" // standing at the manager's desk
  | "to-desk" // walking back after a return
  | "to-coffee" // drifting to the tea machine
  | "coffee" // at the tea machine
  | "at-mailbox"; // waving at the mail box (blocked / permission ask)

export type EmployeeRole =
  | "manager" // the boss (primary opencode session, oikonomos)
  | "hr" // Mnemosyne — roster log
  | "developer" // Tekton
  | "scout" // Skopos (explore)
  | "reviewer" // Dikastes
  | "runner"; // Hemerodromos

export interface Employee {
  id: string; // subagent session id, or generated for fixed seats
  name: string; // desk label: "tekton-1", "scout-2", "dikastes", "hr"
  role: EmployeeRole;
  seat: string; // logical seat id on the floor map
  sprite: SpriteState;
  task?: string; // short label of current brief
}

export type TaskStatus = "pending" | "in-progress" | "done";

export interface BoardTask {
  id: string;
  title: string;
  status: TaskStatus;
  owner?: string; // employee name
  at: number; // epoch ms created
}

export interface MailItem {
  id: string;
  from: string; // employee name | "manager" | "user"
  to: string;
  at: number;
  subject: string;
  body: string; // kept short — full text lives in agentmemory
  kind: "brief" | "return" | "notice" | "user";
}

export interface ChatMsg {
  id: string;
  from: "user" | "boss";
  text: string;
  at: number;
  pending?: boolean; // boss still typing
}

export interface OfficeState {
  employees: Employee[]; // manager + hr always present
  tasks: BoardTask[];
  mails: MailItem[];
  chat: ChatMsg[];
  mode: "live" | "demo";
  statusLine: string; // health/toast text, e.g. "[grafeio] live — opencode 127.0.0.1:4096"
  tick: number; // animation frame counter (backend-agnostic)
}

export type OfficeEvent =
  | { type: "hire"; employee: Employee }
  | { type: "fire"; employeeId: string }
  | { type: "dispatch"; task: BoardTask; employeeId: string }
  | { type: "working"; employeeId: string; taskId? : string }
  | { type: "returned"; employeeId: string; taskId: string; mail: MailItem }
  | { type: "idle-drift"; employeeId: string } // coffee walk
  | { type: "blocked"; employeeId: string; note: string }
  | { type: "task"; task: BoardTask } // board upsert from agentmemory actions
  | { type: "mail"; mail: MailItem } // board/mail sync
  | { type: "chat-user"; msg: ChatMsg }
  | { type: "chat-boss"; msg: ChatMsg }
  | { type: "status"; text: string }
  | { type: "tick" };

export interface OfficeBackend {
  mode: "live" | "demo";
  /** wire events -> app reducer; resolves when listeners are attached */
  start(emit: (e: OfficeEvent) => void): Promise<void>;
  /** user chat -> the boss (real opencode prompt in live mode) */
  send(text: string): Promise<void>;
  stop(): Promise<void>;
}
