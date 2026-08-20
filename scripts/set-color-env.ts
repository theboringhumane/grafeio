/**
 * Imported FIRST by render-snap.ts (ESM evaluates imports in order): force
 * chalk to emit ANSI color even though the harness pipes through a fake,
 * non-TTY stdout. Without this, chalk levels to 0 and all escapes vanish.
 */
process.env.FORCE_COLOR = process.env.FORCE_COLOR ?? "3";
