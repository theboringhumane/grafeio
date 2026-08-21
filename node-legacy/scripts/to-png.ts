/**
 * to-png.ts — repeatable PNG screenshots of the running grafeio app.
 * Capture harness (scripts/ is outside tsconfig include; proof-only).
 *
 *   npx tsx scripts/to-png.ts                      # shots at 1000/2500/4000ms
 *   npx tsx scripts/to-png.ts --label busy --t 2500
 *   npx tsx scripts/to-png.ts --t 1000 --t 4400    (repeatable; commas ok)
 *
 * Pipeline (fallback mode — svg-term "--command" needs an asciinema v1
 * binary, which this repo does not assume):
 *
 *   1. scripts/shot-cast.ts runs the REAL <App> + REAL demo backend through
 *      ink on a fake 120x36 stdout and writes an asciinema v2 cast
 *      (per-row absolute cursor positioning; no newline flow — newline
 *      replay makes svg-term's emulator double-advance on pending wraps).
 *   2. svg-term-cli (npx one-shot) renders the cast at --at <t> to a static
 *      SVG. --no-optimize is REQUIRED: its bundled svgo trims leading
 *      spaces out of styled spans and collapses the topbar/statusbar.
 *   3. SVG -> PNG: chrome-headless-shell from the puppeteer cache when
 *      present (exact font metrics; qlmanage uses a wider fallback font and
 *      clips the last ~15 columns), qlmanage as documented fallback.
 *
 * Output: docs/shots/<label>-<t>.png (+ byte sizes printed).
 */
import {execFileSync} from "node:child_process";
import {existsSync, mkdirSync, readdirSync, readFileSync, renameSync, statSync, writeFileSync} from "node:fs";
import {mkdtempSync} from "node:fs";
import {tmpdir} from "node:os";
import path from "node:path";

// ---------- args ----------
const argv = process.argv.slice(2);
const flagValue = (name: string) => {
  const i = argv.indexOf(`--${name}`);
  return i === -1 ? undefined : argv[i + 1];
};
const allFlagValues = (name: string) => {
  const out: string[] = [];
  for (let i = 0; i < argv.length; i++) if (argv[i] === `--${name}` && argv[i + 1]) out.push(argv[i + 1]);
  return out;
};

const LABEL = flagValue("label") ?? "grafeio";
const tsRaw = allFlagValues("t");
const TIMES =
  tsRaw.length > 0
    ? tsRaw.flatMap((v) => v.split(",")).map((v) => Number(v.trim()))
    : [1000, 2500, 4000];
if (TIMES.some((t) => !Number.isFinite(t) || t < 0)) {
  console.error(`to-png: bad --t value in [${tsRaw.join(", ")}] — expected ms numbers, e.g. --t 2500 or --t 1000,2500`);
  process.exit(1);
}

// ---------- paths ----------
const REPO = path.resolve(import.meta.dirname, "..");
const OUT_DIR = path.join(REPO, "docs", "shots");
const WORK = mkdtempSync(path.join(tmpdir(), "grafeio-png-"));
const COLS = 120;
const ROWS = 36;
/** Demo needs the busiest cast AFTER the last requested --at time. */
const DURATION_MS = Math.max(...TIMES) + 800;

mkdirSync(OUT_DIR, {recursive: true});

// ---------- helpers ----------
const run = (bin: string, args: string[], opts: {input?: Buffer; cwd?: string} = {}) => {
  try {
    return execFileSync(bin, args, {
      cwd: opts.cwd ?? REPO,
      input: opts.input,
      stdio: ["pipe", "pipe", "pipe"],
      maxBuffer: 64 * 1024 * 1024,
    });
  } catch (e: any) {
    const stderr = e?.stderr?.toString?.() ?? "";
    throw new Error(
      `to-png: command failed: ${bin} ${args.join(" ")}\n` +
        `  exit: ${e?.status ?? "?"}\n  stderr: ${stderr.slice(0, 800) || "(empty)"}`,
    );
  }
};

/** Locate a chromium-family binary for faithful SVG rasterization. */
function findChrome(): string | undefined {
  if (process.env.GRAFEIO_CHROME && existsSync(process.env.GRAFEIO_CHROME)) return process.env.GRAFEIO_CHROME;
  const home = process.env.HOME ?? "";
  const pptrRoot = path.join(home, ".cache", "puppeteer", "chrome-headless-shell");
  if (existsSync(pptrRoot)) {
    const versions = readdirSync(pptrRoot)
      .filter((d) => d.startsWith("mac_arm-") || d.startsWith("mac-"))
      .sort((a, b) => b.localeCompare(a, undefined, {numeric: true}));
    for (const v of versions) {
      const bin = path.join(pptrRoot, v, "chrome-headless-shell-mac-arm64", "chrome-headless-shell");
      if (existsSync(bin)) return bin;
    }
  }
  for (const app of ["Google Chrome.app", "Chromium.app", "Google Chrome Canary.app"]) {
    const bin = path.join("/Applications", app, "Contents", "MacOS", app.replace(/\.app$/, ""));
    if (existsSync(bin)) return bin;
  }
  return undefined;
}

/** SVG -> PNG with headless chrome (preferred). Returns false when no chrome. */
function svgToPngChrome(chrome: string, svgPath: string, pngPath: string): void {
  const svg = readFileSync(svgPath, "utf8");
  const mw = svg.match(/<svg[^>]*width="([\d.]+)"[^>]*height="([\d.]+)"/);
  if (!mw) throw new Error(`to-png: cannot read width/height off ${svgPath} — svg-term output unexpected`);
  const w = Math.ceil(Number(mw[1]));
  const h = Math.ceil(Number(mw[2]));
  const isHeadlessShell = path.basename(chrome) === "chrome-headless-shell";
  const args = isHeadlessShell
    ? [
        `--screenshot=${pngPath}`,
        `--window-size=${w},${h}`,
        "--hide-scrollbars",
        "--force-device-scale-factor=2",
        `file://${svgPath}`,
      ]
    : [
        "--headless=new",
        "--disable-gpu",
        `--screenshot=${pngPath}`,
        `--window-size=${w},${h}`,
        "--hide-scrollbars",
        "--force-device-scale-factor=2",
        `file://${svgPath}`,
      ];
  run(chrome, args);
  if (!existsSync(pngPath)) throw new Error(`to-png: chrome reported success but ${pngPath} is missing`);
}

/** SVG -> PNG through macOS Quick Look (documented fallback; loose metrics). */
function svgToPngQlmanage(svgPath: string, pngPath: string): void {
  try {
    execFileSync("qlmanage", ["-t", "-s", "2048", "-o", WORK, svgPath], {stdio: ["pipe", "pipe", "pipe"]});
  } catch {
    // qlmanage exits non-zero on warnings but still writes the thumbnail
  }
  const produced = path.join(WORK, `${path.basename(svgPath)}.png`);
  if (!existsSync(produced)) {
    throw new Error(
      `to-png: qlmanage produced no thumbnail for ${svgPath}.\n` +
        `  Try installing chrome (or set GRAFEIO_CHROME=/path/to/chrome) for the high-fidelity path.`,
    );
  }
  renameSync(produced, pngPath);
}

// ---------- 1. build the cast ----------
const castPath = path.join(WORK, `${LABEL}.cast`);
console.log(`to-png: capturing real demo session (${DURATION_MS}ms, ${COLS}x${ROWS}) -> cast`);
run("npx", ["tsx", path.join("scripts", "shot-cast.ts"), castPath, String(DURATION_MS)]);
const cast = readFileSync(castPath);
{
  const firstLine = cast.toString("utf8").split("\n")[0];
  if (!firstLine.includes('"version":2')) {
    throw new Error(`to-png: ${castPath} does not look like an asciinema v2 cast:\n  ${firstLine.slice(0, 120)}`);
  }
}

// ---------- 2+3. render each timepoint ----------
const chrome = findChrome();
console.log(
  `to-png: rasterizer: ${chrome ? `chrome (${chrome.split("/").slice(-1)[0]})` : "qlmanage (fallback — font metrics approximate)"}`,
);

let failures = 0;
const produced: Array<{png: string; bytes: number}> = [];
for (const t of TIMES) {
  const svgPath = path.join(WORK, `${LABEL}-${t}.svg`);
  try {
    run("npx", [
      "--yes",
      "svg-term-cli",
      "--at",
      String(t),
      "--window",
      "--no-optimize", // svgo (default optimise) trims leading spaces out of styled spans
      "--out",
      svgPath,
    ], {input: cast});
    const svg = readFileSync(svgPath, "utf8");
    // --no-optimize leaves colors as rgb(); optimized output uses #hex. Count both.
    const colors = (svg.match(/#[0-9a-fA-F]{3,6}|rgb\(/g) ?? []).length;
    if (svg.length < 2000) throw new Error(`svg looks empty (${svg.length} bytes) — capture probably failed`);
    if (colors < 8) throw new Error(`only ${colors} hex colors in svg — ANSI color was lost upstream`);

    const pngPath = path.join(OUT_DIR, `${LABEL}-${t}.png`);
    if (chrome) svgToPngChrome(chrome, svgPath, pngPath);
    else svgToPngQlmanage(svgPath, pngPath);

    // keep the svg next to the png for debugging / color greps
    writeFileSync(path.join(OUT_DIR, `${LABEL}-${t}.svg`), svg);
    const bytes = statSync(pngPath).size;
    produced.push({png: pngPath, bytes});
    console.log(`to-png: t=${t}ms -> ${path.relative(REPO, pngPath)} (${bytes} bytes, svg color refs x${colors})`);
  } catch (e: any) {
    failures++;
    console.error(`to-png: FAILED t=${t}ms: ${e?.message ?? e}`);
  }
}

if (failures > 0) {
  console.error(`to-png: ${failures}/${TIMES.length} shots failed`);
  process.exit(1);
}
console.log(`to-png: OK — ${produced.length} pngs in docs/shots/`);
