// The game model: one game as a plain JSON document, plus the operations on
// it. No DOM here. The same document is what IndexedDB stores and what sync
// sends to the server, so everything a device needs to resume a game lives
// in it.

export const LEVELS = ["easy", "medium", "hard", "expert"];

// Undo history beyond this many moves is dropped, oldest first. It bounds
// the synced document (the server caps one at 64 KB).
const HISTORY_LIMIT = 300;

export const row = (c) => Math.floor(c / 9);
export const col = (c) => c % 9;
export const box = (c) => Math.floor(c / 27) * 3 + Math.floor((c % 9) / 3);

// peers[c] lists the 20 cells that share a row, column, or box with c.
export const peers = Array.from({ length: 81 }, (_, c) => {
  const out = [];
  for (let p = 0; p < 81; p++) {
    if (p !== c && (row(p) === row(c) || col(p) === col(c) || box(p) === box(c))) out.push(p);
  }
  return out;
});

// Houses in the server's numbering: 0–8 rows, 9–17 columns, 18–26 boxes.
export function unitCells(u) {
  const out = [];
  for (let c = 0; c < 81; c++) {
    if ((u < 9 && row(c) === u) || (u >= 9 && u < 18 && col(c) === u - 9) || (u >= 18 && box(c) === u - 18)) {
      out.push(c);
    }
  }
  return out;
}

const parseGrid = (s) => Array.from(s, (ch) => (ch === "." ? 0 : Number(ch)));
const gridString = (a) => a.map((v) => (v ? String(v) : ".")).join("");

/**
 * Builds a new game document from a /api/puzzle response.
 * @param {{level: string, givens: string, solution: string, steps: object[]}} p
 */
export function newGame(p) {
  const now = Date.now();
  return {
    id: crypto.randomUUID(),
    level: p.level,
    givens: p.givens,
    solution: p.solution,
    // The grader's solving path, kept for hints when the server is out of
    // reach. See hint.js.
    steps: p.steps,
    entries: parseGrid(p.givens),
    notes: new Array(81).fill(0), // bit d set: pencil mark d
    history: [],
    elapsed: 0, // milliseconds of play
    hints: 0,
    checks: 0,
    status: "playing", // "playing" | "won" | "abandoned"
    created: now,
    updated: now,
  };
}

export const isGiven = (g, c) => g.givens[c] !== ".";
export const solutionAt = (g, c) => Number(g.solution[c]);
export const board = (g) => gridString(g.entries);

/** Snapshot the cells a move will touch, for undo. */
function snapshot(g, cells) {
  return cells.map((c) => [c, g.entries[c], g.notes[c]]);
}

function record(g, snap) {
  g.history.push(snap);
  if (g.history.length > HISTORY_LIMIT) g.history.splice(0, g.history.length - HISTORY_LIMIT);
  g.updated = Date.now();
}

/**
 * Places digit d in cell c, or clears it if d is already there. With
 * autoNotes, the digit also leaves the notes of every peer.
 * @returns {boolean} whether anything changed
 */
export function setDigit(g, c, d, autoNotes) {
  if (isGiven(g, c) || g.status !== "playing") return false;
  const clearing = g.entries[c] === d;
  const touched = [c];
  if (!clearing && autoNotes) {
    for (const p of peers[c]) if (g.notes[p] & (1 << d)) touched.push(p);
  }
  record(g, snapshot(g, touched));
  g.entries[c] = clearing ? 0 : d;
  g.notes[c] = 0;
  if (!clearing && autoNotes) {
    for (const p of touched.slice(1)) g.notes[p] &= ~(1 << d);
  }
  return true;
}

/** Toggles pencil mark d in an empty cell c. */
export function toggleNote(g, c, d) {
  if (isGiven(g, c) || g.entries[c] !== 0 || g.status !== "playing") return false;
  record(g, snapshot(g, [c]));
  g.notes[c] ^= 1 << d;
  return true;
}

/** Clears the player's digit or notes from cell c. */
export function erase(g, c) {
  if (isGiven(g, c) || g.status !== "playing") return false;
  if (g.entries[c] === 0 && g.notes[c] === 0) return false;
  record(g, snapshot(g, [c]));
  g.entries[c] = 0;
  g.notes[c] = 0;
  return true;
}

/** Reverts the last move. @returns {number|null} a cell the move touched */
export function undo(g) {
  if (g.status !== "playing") return null;
  const snap = g.history.pop();
  if (!snap) return null;
  for (const [c, v, n] of snap) {
    g.entries[c] = v;
    g.notes[c] = n;
  }
  g.updated = Date.now();
  return snap[0][0];
}

/** Cells holding a player digit that disagrees with the solution. */
export function wrongCells(g) {
  const out = [];
  for (let c = 0; c < 81; c++) {
    if (g.entries[c] && !isGiven(g, c) && g.entries[c] !== solutionAt(g, c)) out.push(c);
  }
  return out;
}

/**
 * Cells whose digit repeats in their row, column, or box. Both cells of a
 * pair are included, givens too: the rules alone show the clash, so it
 * isn't a hint about which digit is wrong.
 */
export function clashCells(g) {
  const out = new Set();
  for (let c = 0; c < 81; c++) {
    const v = g.entries[c];
    if (v && peers[c].some((p) => g.entries[p] === v)) out.add(c);
  }
  return out;
}

export function isSolved(g) {
  return g.entries.every((v, c) => v === solutionAt(g, c));
}

/** How many of each digit are on the board: counts[d] for d in 1–9. */
export function digitCounts(g) {
  const counts = new Array(10).fill(0);
  for (const v of g.entries) counts[v]++;
  return counts;
}

export function formatTime(ms) {
  const s = Math.floor(ms / 1000);
  const h = Math.floor(s / 3600);
  const m = Math.floor((s % 3600) / 60);
  const ss = String(s % 60).padStart(2, "0");
  return h ? `${h}:${String(m).padStart(2, "0")}:${ss}` : `${m}:${ss}`;
}
