/**
 * shot-run.ts — bounded demo run for screenshot capture (scripts/to-png.ts).
 * Not typechecked (scripts/ is outside tsconfig include); capture harness.
 *
 * Runs the real App against the real demo backend on the REAL stdout — inside
 * svg-term's pty it is a TTY, so the App borrows the alternate screen exactly
 * like `grafeio --demo` does — then exits cleanly after ~6s. Shutdown goes
 * through the same path as src/index.ts: backend.stop() -> app.unmount() —
 * the App's unmount cleanup restores the alt-screen; nothing is bypassed.
 *
 *   SHOT_DURATION_MS=6000 npx tsx scripts/shot-run.ts
 */
import "./set-color-env.js"; // FIRST: force chalk ANSI even if the pipe lies
import React from "react";
import {render} from "ink";
import {App} from "../src/app.js";
import {createDemoBackend} from "../src/backend/demo.js";

const DURATION_MS = Number(process.env.SHOT_DURATION_MS ?? 6000);
// App mounts AFTER backend.start()'s useEffect ticks begin; give the first
// frames a moment to land, then keep the busy window (~1s..4s) on screen.
const extraMs = Number(process.env.SHOT_TAIL_MS ?? 150);

async function main() {
  if (!process.stdout.isTTY) {
    console.error(
      "shot-run: stdout is not a TTY — this harness records a real pty session " +
        "(svg-term command mode). For piped capture use scripts/to-png.ts cast mode.",
    );
    process.exit(1);
  }

  const backend = createDemoBackend();
  const app = render(React.createElement(App, {backend}));

  let shuttingDown = false;
  const shutdown = async () => {
    if (shuttingDown) return;
    shuttingDown = true;
    await backend.stop();
    app.unmount(); // App effect cleanup restores the alt-screen here
    setTimeout(() => process.exit(0), extraMs); // let the restore write flush
  };
  process.on("SIGINT", shutdown);
  process.on("SIGTERM", shutdown);
  setTimeout(() => void shutdown(), DURATION_MS);
}

main().catch((e) => {
  console.error(`[shot-run] fatal: ${e?.message ?? e}`);
  process.exit(1);
});
