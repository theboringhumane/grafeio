/**
 * smoke-demo.ts — backend smoke test for the demo timeline.
 *
 * Records ~6.5s of events from createDemoBackend() and asserts the floor
 * contract: status, hires (incl. hr), dispatches with tasks, working
 * pulses, a returned employee carrying return-mail with a body, a blocked
 * moment, and board upserts. Prints "SMOKE OK" plus a compact event log.
 *
 * Run: npx tsx scripts/smoke-demo.ts   (exit 0 = pass, 1 = assertion failed)
 */
import { createDemoBackend } from "../src/backend/demo.js";
import type { OfficeEvent } from "../src/state.js";

const RUN_MS = 6_800;

const events: OfficeEvent[] = [];
const backend = createDemoBackend();
await backend.start((e) => events.push(e));
await new Promise((r) => setTimeout(r, RUN_MS));
await backend.stop();

// Trailing events that fired exactly at the boundary may race stop();
// drain one macrotask so the assertion set is stable.
await new Promise((r) => setTimeout(r, 20));

const failures: string[] = [];
function check(cond: boolean, label: string): void {
  if (!cond) failures.push(label);
}

// --- the contract, in order -------------------------------------------------
const hires = events.filter((e) => e.type === "hire");
const dispatches = events.filter((e) => e.type === "dispatch");
const workPulses = events.filter((e) => e.type === "working");
const returns = events.filter((e) => e.type === "returned");
const blocked = events.filter((e) => e.type === "blocked");
const upserts = events.filter((e) => e.type === "task" || e.type === "dispatch");

check(events.some((e) => e.type === "status"), "expected >=1 status event");
check(hires.length >= 3, `expected >=3 hire events, got ${hires.length}`);
check(
  hires.some((e) => e.type === "hire" && (e.employee.role === "hr" || e.employee.name === "hr")),
  "expected an hr hire among the hires",
);
check(dispatches.length >= 2, `expected >=2 dispatch events, got ${dispatches.length}`);
check(
  dispatches.every((e) => e.type === "dispatch" && !!e.task?.title && e.task.status === "in-progress"),
  "every dispatch must carry an in-progress task with a title",
);
check(workPulses.length >= 1, "expected >=1 working event");
check(returns.length >= 1, "expected >=1 returned event");
check(
  returns.some((e) => e.type === "returned" && !!e.mail && e.mail.body.trim().length > 0),
  "expected a returned event carrying mail with a non-empty body",
);
check(blocked.length >= 1, "expected >=1 blocked event");
check(upserts.length >= 2, `expected >=2 task upserts (task|dispatch), got ${upserts.length}`);

// --- compact log -------------------------------------------------------------
function oneLine(e: OfficeEvent, i: number): string {
  const n = String(i).padStart(3, " ");
  switch (e.type) {
    case "status": return `[${n}] status    ${e.text}`;
    case "hire": return `[${n}] hire      ${e.employee.name} (${e.employee.role}) seat=${e.employee.seat}`;
    case "dispatch": return `[${n}] dispatch  ${e.employeeId} task=${e.task.id} "${e.task.title}" [${e.task.status}]`;
    case "working": return `[${n}] working   ${e.employeeId}${e.taskId ? ` task=${e.taskId}` : ""}`;
    case "returned": return `[${n}] returned  ${e.employeeId} task=${e.taskId} mail="${e.mail.subject}" body=${e.mail.body.length}chars`;
    case "blocked": return `[${n}] blocked   ${e.employeeId} note="${e.note}"`;
    case "task": return `[${n}] task      ${e.task.id} "${e.task.title}" [${e.task.status}] owner=${e.task.owner ?? "-"}`;
    case "mail": return `[${n}] mail      ${e.mail.from} -> ${e.mail.to} "${e.mail.subject}"`;
    case "idle-drift": return `[${n}] coffee    ${e.employeeId}`;
    case "chat-user": return `[${n}] chat-user "${e.msg.text}"`;
    case "chat-boss": return `[${n}] chat-boss "${e.msg.text}"${e.msg.pending ? " (pending)" : ""}`;
    case "fire": return `[${n}] fire      ${e.employeeId}`;
    case "tick": return `[${n}] tick`;
  }
}

const ticks = events.filter((e) => e.type === "tick").length;
const shown = events.filter((e) => e.type !== "tick");
console.log("--- demo event log (ticks collapsed) ---");
shown.forEach((e) => console.log(oneLine(e, events.indexOf(e))));
console.log(`[...] tick      x${ticks} (180ms interval, collapsed)`);
console.log(`--- ${events.length} events in ${RUN_MS}ms ---`);

if (failures.length > 0) {
  console.error(`SMOKE FAIL (${failures.length}):`);
  for (const f of failures) console.error(`  - ${f}`);
  process.exit(1);
}
console.log(`SMOKE OK - ${shown.length} scripted events + ${ticks} ticks, all assertions passed`);
process.exit(0);
