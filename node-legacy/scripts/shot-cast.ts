/**
 * shot-cast.ts — synthesize an asciinema v2 cast of the REAL App (+ real demo
 * backend) without a pty. Capture harness (scripts/ is outside tsconfig).
 *
 *   npx tsx scripts/shot-cast.ts <out.cast> [durationMs]
 *
 * Technique = render-shell.ts: mount <App> through ink with a fake 120x36
 * stdout (ink-testing-library pins 100 cols), record every frame write with
 * its timestamp, then re-emit the session as an asciinema v2 cast: one "o"
 * event per frame, each prefixed with a clear-screen ("\x1b[2J\x1b[H") so
 * every event is a full repaint. svg-term-cli reads this on stdin.
 */
import "./set-color-env.js"; // FIRST: force chalk ANSI under the piped harness
import React from "react";
import {EventEmitter} from "node:events";
import {writeFileSync} from "node:fs";
import {render as inkRender} from "ink";
import {App} from "../src/app.js";
import {createDemoBackend} from "../src/backend/demo.js";

const COLS = 120;
const ROWS = 36;

const outPath = process.argv[2];
const DURATION_MS = Number(process.argv[3] ?? 4600);
if (!outPath) {
  console.error("shot-cast: usage: npx tsx scripts/shot-cast.ts <out.cast> [durationMs]");
  process.exit(1);
}

const sleep = (ms: number) => new Promise((r) => setTimeout(r, ms));

/** Minimal ink-compatible stdout pinned to COLS x ROWS (see render-shell.ts). */
class FakeStdout extends EventEmitter {
  get columns() {
    return COLS;
  }
  get rows() {
    return ROWS;
  }
  frames: Array<{t: number; frame: string}> = [];
  private start = Date.now();
  write = (frame: string) => {
    this.frames.push({t: (Date.now() - this.start) / 1000, frame});
  };
}

class FakeStderr extends EventEmitter {
  frames: string[] = [];
  write = (frame: string) => {
    this.frames.push(frame);
  };
}

class FakeStdin extends EventEmitter {
  isTTY = true;
  setEncoding() {}
  setRawMode() {}
  resume() {}
  pause() {}
  ref() {}
  unref() {}
}

async function main() {
  const stdout = new FakeStdout();
  const inst = inkRender(
    React.createElement(App, {
      backend: createDemoBackend(),
      cols: COLS,
      rows: ROWS,
    }),
    {
      stdout: stdout as never,
      stderr: new FakeStderr() as never,
      stdin: new FakeStdin() as never,
      debug: true, // write full frames to the fake stdout
      exitOnCtrlC: false,
      patchConsole: false,
    },
  );

  await sleep(DURATION_MS); // run the real demo backend in real time
  inst.unmount();
  await sleep(50);

  if (stdout.frames.length < 3) {
    console.error(`shot-cast: only ${stdout.frames.length} frames captured — refusing to write cast`);
    process.exit(1);
  }

  const header = {
    version: 2,
    width: COLS,
    height: ROWS,
    timestamp: Math.floor(Date.now() / 1000),
    env: {TERM: "xterm-256color", SHELL: "zsh"},
  };
  // Emit each frame as: clear screen, then one absolute cursor-position
  // (CUP) write per row — NO newline flow. Rows are exactly COLS wide, which
  // leaves the cursor in a pending-wrap state; newline-based replay makes
  // over-eager emulators (svg-term's) double-advance rows and drop content.
  // Explicit CUP cancels pending wrap and is unambiguous everywhere.
  const rowByRow = (frame: string) => {
    const rows = frame.replace(/\n+$/, "").split("\n");
    return (
      "\x1b[2J" + rows.map((r, i) => `\x1b[${i + 1};1H${r}`).join("")
    );
  };
  const events = stdout.frames.map(({t, frame}) => [t, "o", rowByRow(frame)]);
  const cast =
    JSON.stringify(header) + "\n" + events.map((e) => JSON.stringify(e)).join("\n") + "\n";
  writeFileSync(outPath, cast);
  console.error(
    `shot-cast: wrote ${stdout.frames.length} frames over ${DURATION_MS}ms -> ${outPath}`,
  );
  process.exit(0);
}

await main();
