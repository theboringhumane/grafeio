#!/usr/bin/env node
/**
 * grafeio — the terminal office. Entry point.
 *   grafeio            live mode: spawn/attach `opencode serve` for <cwd>
 *   grafeio --demo     touring mode: simulated events (explicitly labeled)
 *   grafeio --server http://127.0.0.1:PORT   attach, don't spawn
 */
import React from "react";
import { render } from "ink";
import { App } from "./app.js";
import { createLiveBackend } from "./backend/opencode.js";
import { createDemoBackend } from "./backend/demo.js";

const args = process.argv.slice(2);
const flag = (name: string) => {
  const i = args.indexOf(`--${name}`);
  return i === -1 ? undefined : args[i + 1];
};

const demo = args.includes("--demo") || process.env.GRAFEIO_DEMO === "1";
const server = flag("server");
const directory = process.cwd();

async function main() {
  const backend = demo
    ? createDemoBackend()
    : createLiveBackend({ baseUrl: server, directory });

  // App mounts listeners via backend.start() inside useEffect.
  const app = render(React.createElement(App, { backend }));
  const shutdown = async () => {
    await backend.stop();
    app.unmount();
    process.exit(0);
  };
  process.on("SIGINT", shutdown);
  process.on("SIGTERM", shutdown);
}

main().catch((e) => {
  console.error(`[grafeio] fatal: ${e?.message ?? e}`);
  process.exit(1);
});
