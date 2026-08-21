/**
 * floorplan.ts — props-driven office floor geometry.
 *
 * computePlan(width, height) -> Plan:
 *   zones:    walled rooms/partitions with door gaps
 *             (boss office, conference, server room, glass cabins, break area)
 *   props:    furniture glyphs ("[=BOSS=]" desk, "=[=======" table,
 *             "[||]" racks, plants, bins, cooler, mail tray...)
 *   anchors:  Map<seatId, {x,y}> — where sprites park at home
 *   hotspots: meet/mail/tea/clock/overflow targets for walkers + wall
 *
 * Degrades gracefully, NEVER throws:
 *   - cabins drop first (need W>=96 && H>=24)
 *   - conference drops next (need W>=72 && H>=18)
 *   - pods row minimum: 2 rows x 4 pods, collapsing to 2x3 when cabins are gone
 *   - W<60 || H<16: "small terminal" badge (drawn once, centered, by floor.tsx)
 *
 * Props and zones may carry a chalk foreground color (and bold on props);
 * floor.tsx stamps them onto its styled cell grid. Default paint:
 * drywall zone walls gray; unstyled props render uncolored.
 */
import type {ForegroundColorName} from "chalk";

/** Ink/chalk foreground color names ("yellow" | "cyan" | "gray" | ...). */
export type ColorName = ForegroundColorName;

export interface Point {
  x: number;
  y: number;
}

export interface Door {
  side: "n" | "s" | "e" | "w";
  at: number; // offset along that side from the zone origin corner
  size: number; // width of the gap in cells
}

export interface Zone {
  id: string;
  x: number;
  y: number;
  w: number;
  h: number;
  wall: string; // "-" for drywall rooms; cabins use ":", ";", "."
  color?: ColorName; // wall paint; default gray (applied by floor.tsx)
  doors: Door[];
}

export interface PlanProp {
  x: number;
  y: number;
  glyph: string;
  color?: ColorName; // furniture paint; undefined = uncolored
  bold?: boolean;
  over?: boolean; // stamp AFTER zone walls (wall-mounted items: windows)
}

export interface Plan {
  width: number;
  height: number;
  gen: number; // monotonic: walkers re-seat when this changes
  zones: Zone[];
  props: PlanProp[];
  anchors: Map<string, Point>;
  hotspots: {
    meet: Point; // in front of the boss desk (dispatch briefings)
    mail: Point; // at the mail tray (blocked / permission asks)
    tea: Point; // at the coffee machine (idle drift)
    clock: Point; // on the boss-office wall
    overflow: Point; // base spot for "floor-<n>" overflow near the break area
  };
  nameplate: Point; // top-left of the boss-office nameplate (status text, 10 cols)
  tiny: boolean; // draw the "small terminal" badge
}

const clamp = (v: number, lo: number, hi: number) => Math.max(lo, Math.min(hi, v));

let GEN = 0;
const cache = new Map<string, Plan>();
let current: Plan | null = null;

/**
 * Office wall clock: 09:00 start, +1 minute per 30 ticks -> "(9:41)".
 * NOTE: the app shell duplicates this formula today; acceptable per plan
 * (shell renders its own surface), but it belongs here long-term.
 */
export function tickClock(tick: number): string {
  const total = 9 * 60 + Math.floor(Math.max(0, tick) / 30);
  const hh = Math.floor(total / 60) % 24;
  const mm = total % 60;
  return `(${hh}:${String(mm).padStart(2, "0")})`;
}

export function computePlan(width: number, height: number): Plan {
  const key = `${Math.floor(width)}x${Math.floor(height)}`;
  const hit = cache.get(key);
  if (hit) {
    current = hit;
    return hit;
  }
  const p = buildPlan(Math.max(24, Math.floor(width)), Math.max(12, Math.floor(height)));
  if (cache.size > 8) cache.clear();
  cache.set(key, p);
  current = p;
  return p;
}

/** The plan most recently computed (walkers live against this). */
export function currentPlan(): Plan {
  if (!current) current = computePlan(120, 28);
  return current;
}

function buildPlan(W: number, H: number): Plan {
  const gen = ++GEN;
  const zones: Zone[] = [];
  const props: PlanProp[] = [];
  const anchors = new Map<string, Point>();

  const tiny = W < 60 || H < 16;
  // glass cabins survive down to W=72; below W=88 they go compact (~11 wide)
  // so all three still fit the middle band at the real 84-col shell size
  const cabinsOn = W >= 72 && H >= 24;
  const cabinsCompact = W < 88;
  const confOn = W >= 72 && H >= 18;

  const topH = clamp(Math.floor((H - 2) * 0.34), 5, 9);
  const bw = clamp(Math.floor(W * 0.26), 15, 30);
  const sw = clamp(Math.floor(W * 0.2), 13, 24);

  // ---- BOSS CORNER OFFICE (top-left) ----
  zones.push({
    id: "boss",
    x: 1,
    y: 1,
    w: bw,
    h: topH,
    wall: "-",
    doors: [{side: "s", at: Math.floor(bw / 2), size: 2}],
  });
  const nameplate: Point = {x: 3, y: 2};
  // top-wall window, right of the clock, never past the corner (over: stamped
  // after the zone walls so the drywall doesn't erase it)
  const winX = Math.min(1 + Math.floor(bw / 2) + 2, bw - 8);
  props.push(
    // nameplate: layout placeholder; floor.tsx re-stamps it with the
    // boss's live status ("[awaiting]" / "[typing]" / "[meetin]")
    {x: nameplate.x, y: nameplate.y, glyph: "[awaiting]", color: "yellow", bold: true},
    {x: 3, y: 3, glyph: "[=BOSS=]"}, // boss desk
    {x: 14, y: 3, glyph: "(~)", color: "white"}, // desk-side mug
    {x: winX, y: 1, glyph: "[==o==]", color: "cyan", over: true}, // window
    {x: 3, y: 5, glyph: "o", color: "green"}, // guest chair
    {x: 8, y: 5, glyph: "o", color: "green"}, // guest chair
    {x: 10, y: 1, glyph: "[###]", color: "gray"}, // wall calendar
  );
  if (bw >= 20) {
    props.push(
      {x: 13, y: 2, glyph: "[#]", color: "yellow"}, // bookshelf, beside the nameplate
      {x: 16, y: 2, glyph: "[=]", color: "yellow"}, // books, second shelf cell
    );
  }
  anchors.set("manager", {x: 11, y: 3});
  const meet: Point = {x: 5, y: 4};
  const clock: Point = {x: 3, y: 1};

  // ---- SERVER / PRINT ROOM (top-right) ----
  const sx = W - 1 - sw;
  zones.push({
    id: "server",
    x: sx,
    y: 1,
    w: sw,
    h: topH,
    wall: "-",
    doors: [{side: "s", at: 3, size: 2}],
  });
  props.push(
    {x: sx + 2, y: 2, glyph: "[=]", color: "gray"}, // pod 1 monitor (screen off)
    {x: sx + 1, y: 3, glyph: "[______]"}, // pod 1 desk
    {x: sx + 1, y: topH - 1, glyph: "[cpr]", color: "gray"}, // copier
    {x: sx + 1, y: topH - 2, glyph: "[o==o]"}, // treadmill (runner seat desk)
  );
  if (topH >= 8) {
    props.push(
      {x: sx + 2, y: 4, glyph: "[=]", color: "gray"}, // pod 2 monitor
      {x: sx + 1, y: 5, glyph: "[______]"}, // pod 2 desk
    );
  }
  // racks: single-file, one-row spacing, max 2 columns (2nd column staggered)
  for (let r = 2; r <= topH - 1; r += 2) props.push({x: sx + sw - 5, y: r, glyph: "[||]", color: "magenta"});
  if (sw >= 20)
    for (let r = 3; r <= topH - 1; r += 2) props.push({x: sx + sw - 10, y: r, glyph: "[||]", color: "magenta"});
  // power box between the desk pod and the rack column
  if (sw >= 15 && sw < 20) props.push({x: sx + 9, y: 3, glyph: "[ups]", color: "magenta"});
  else if (sw >= 20) props.push({x: sx + 9, y: 4, glyph: "[ups]", color: "magenta"});
  anchors.set("treadmill-1", {x: sx + 2, y: topH - 2});

  // ---- CONFERENCE ROOM (top-center) ----
  const fx = 1 + bw;
  const fw = sx - fx;
  if (confOn && fw >= 18) {
    zones.push({
      id: "conference",
      x: fx,
      y: 1,
      w: fw,
      h: topH,
      wall: "-",
      doors: [{side: "s", at: Math.floor(fw / 2), size: 2}],
    });
    const ty = 1 + Math.floor(topH / 2);
    // table leaves the easel its right-hand 8 cols (2-col wall clearance included)
    const tlen = clamp(fw - 12, 6, 14);
    props.push({x: fx + 3, y: ty, glyph: "=[" + "=".repeat(Math.max(0, tlen - 3)) + "]", color: "yellow"});
    const nchairs = clamp(Math.floor((fw - 8) / 6), 2, 8);
    const up = Math.ceil(nchairs / 2);
    const down = nchairs - up;
    for (let i = 0; i < up; i++) props.push({x: fx + 4 + i * 3, y: ty - 1, glyph: "o", color: "green"});
    for (let i = 0; i < down; i++) props.push({x: fx + 4 + i * 3, y: ty + 1, glyph: "o", color: "green"});
    props.push(
      {x: fx + 1, y: topH - 1, glyph: "(Y)", color: "green"}, // corner plant
      {x: fx + fw - 8, y: 2, glyph: "[|> ]", color: "cyan"}, // easel / whiteboard (2-col clearance from the right wall)
      {x: fx + fw - 7, y: 3, glyph: sslash()}, // tiny chart marks
    );
  }

  // ---- GLASS CABINS (middle band) — walls ":", ";", "." ----
  const cabY = 2 + topH;
  const cabIds = ["hr", "cabin-2", "cabin-3"];
  const cw = cabinsCompact ? 11 : 13; // compact glass cabins keep all 3 at W<88
  const cstep = cabinsCompact ? 14 : 16;
  if (cabinsOn) {
    for (let i = 0; i < 3; i++) {
      const cx = 3 + i * cstep;
      zones.push({
        id: `cabin-${i + 1}`,
        x: cx,
        y: cabY,
        w: cw,
        h: 6,
        wall: [":", ";", "."][i],
        color: (["magenta", "blue", "magentaBright"] as const)[i], // glass tint per cabin
        doors: [{side: "s", at: Math.floor(cw / 2), size: 2}],
      });
      props.push(
        {x: cx + 4, y: cabY + 1, glyph: "[=]", color: "gray"},
        {x: cx + 2, y: cabY + 2, glyph: "[=____=]"},
        {x: cx + 4, y: cabY + 3, glyph: "(_)", color: "green"},
        {x: cx + cw - 3, y: cabY + 1, glyph: "(Y)", color: "green"},
      );
      anchors.set(cabIds[i], {x: cx + 4, y: cabY + 3});
    }
  } else {
    // cabins collapsed: freestanding desks for HR + reviewer on the freed band
    ["hr", "cabin-2"].forEach((id, i) => {
      const cx = 2 + i * 14;
      props.push(
        {x: cx + 2, y: cabY, glyph: "[=]", color: "gray"},
        {x: cx, y: cabY + 1, glyph: "[______]"},
        {x: cx + 2, y: cabY + 2, glyph: "(_)", color: "green"},
      );
      anchors.set(id, {x: cx + 2, y: cabY + 2});
    });
  }

  // ---- BREAK AREA (bottom-right) ----
  const bwd = clamp(Math.floor(W * 0.19), 14, 26);
  const bx0 = W - 1 - bwd;
  const by0 = H - 9;
  zones.push({
    id: "break",
    x: bx0,
    y: by0,
    w: bwd,
    h: 8,
    wall: "-",
    doors: [
      {side: "n", at: 3, size: 3},
      {side: "w", at: 4, size: 2},
    ],
  });
  props.push(
    {x: bx0 + 2, y: by0 + 1, glyph: "[===]", color: "yellow"}, // fridge
    {x: bx0 + 8, y: by0 + 1, glyph: "[bin]", color: "gray"}, // recycle bin near the kitchen
    {x: bx0 - 4, y: by0 + 4, glyph: "brk", color: "gray"}, // door-gap label on the left partition
    {x: bx0 + 2, y: by0 + 2, glyph: "[#####]", color: "yellow"}, // kitchen counter
    {x: bx0 + 8, y: by0 + 2, glyph: "[cof]", color: "yellow"}, // coffee machine on the counter
    bwd >= 20
      ? {x: bx0 + bwd - 6, y: by0 + 2, glyph: "[vnd]"} // vending machine (right wall)
      : {x: bx0 + 2, y: by0 + 5, glyph: "[vnd]"}, // narrow room: under the fridge
    {x: bx0 + bwd - 6, y: by0 + 4, glyph: "[cpy]", color: "magenta"}, // rack
    {x: bx0 + 10, y: by0 + 5, glyph: "("}, // stool
    {x: bx0 + 11, y: by0 + 5, glyph: "(__)"}, // small table
    {x: bx0 + 15, y: by0 + 5, glyph: ")"}, // stool
    {x: bx0 + 2, y: by0 + 6, glyph: "[mail]"}, // mail tray
  );
  const tea: Point = {x: bx0 + 8, y: by0 + 3};
  const mail: Point = {x: bx0 + 2, y: by0 + 5};
  const overflow: Point = {x: bx0 + 2, y: by0 - 2};

  // ---- DEV POD FIELD (bottom-left/center) ----
  // pod: 8 wide x 3 tall — "[=]" monitor / "[______]" desk / "(_)" chair
  const fieldRight = bx0 - 2;
  const nMin = cabinsOn ? 4 : 3;
  const nb = Math.max(nMin, Math.floor((fieldRight - 3 - 8) / 11) + 1);
  const podRows = [H - 9, H - 5];
  const scoutIdx = [nb - 1, 2 * nb - 1]; // right-side pods (skopos)
  let devN = 0;
  for (let i = 0; i < 2 * nb; i++) {
    const r = i < nb ? 0 : 1;
    const px = 3 + (i % nb) * 11;
    const py = podRows[r];
    props.push(
      {x: px + 3, y: py, glyph: "[=]", color: "gray"}, // monitor (screen off; lit cyan bold by floor.tsx when a dev works here)
      {x: px, y: py + 1, glyph: "[______]"},
      {x: px + 2, y: py + 2, glyph: "(_)", color: "green"},
    );
    if (i === scoutIdx[0]) anchors.set("scout-1", {x: px + 2, y: py + 2});
    else if (i === scoutIdx[1]) anchors.set("scout-2", {x: px + 2, y: py + 2});
    else anchors.set(`dev-${++devN}`, {x: px + 2, y: py + 2});
  }

  // ---- MIDDLE-BAND PARTITION STRIP (corridor between cabins and pod rows) ----
  // low wall fragments flanking a hanging whiteboard + 2 plants; decor only,
  // no zones/no hotspots, walkers stay unblocked. Drawn only when a free
  // corridor row exists (>= 1 blank row above the first pod row).
  const stripY = cabY + 6;
  if (stripY <= H - 10) {
    const cabCenter = 3 + Math.floor((2 * cstep + cw) / 2);
    const wbX = cabCenter - 3; // hanging whiteboard, roughly band-centered
    props.push(
      {x: wbX - 11, y: stripY, glyph: "+--- ---+", color: "gray"}, // low wall, left
      {x: wbX, y: stripY, glyph: "[plan]", color: "yellow"}, // hanging whiteboard
      {x: wbX + 8, y: stripY, glyph: "+--- ---+", color: "gray"}, // low wall, right
      {x: cabCenter - 17, y: stripY, glyph: "(Y)", color: "green"}, // plant, left
      {x: cabCenter + 22, y: stripY, glyph: "(Y)", color: "green"}, // plant, right
    );
  }

  // ---- SCATTER: plants, bins, water cooler ----
  props.push(
    {x: 2, y: H - 3, glyph: "[h2o]", color: "blue"}, // water cooler
    {x: 2, y: H - 2, glyph: "(Y)", color: "green"}, // plant, bottom-left corner
    {x: 1, y: podRows[0] + 1, glyph: "(.)", color: "gray"}, // trash near pod row 1
    {x: 1, y: podRows[1] + 1, glyph: "(.)", color: "gray"}, // trash near pod row 2
    {x: bx0 - 2, y: H - 2, glyph: "(Y)", color: "green"}, // plant by the break door
  );

  return {width: W, height: H, gen, zones, props, anchors, hotspots: {meet, mail, tea, clock, overflow}, nameplate, tiny};
}

/** tiny chart marks under the whiteboard (kept as a fn so the quoting stays obvious). */
function sslash(): string {
  return "|/_";
}
