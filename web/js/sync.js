// Server communication: the puzzle buffer, sign-in, and sync.
//
// Sync rule: each game carries the server revision it was based on. A push
// with a stale revision is refused, and the device adopts the server's copy.
// The first device to sync a change wins; the other loses its unsynced
// moves on that game and is told so.

import * as db from "./db.js";
import { LEVELS } from "./game.js";

// Unplayed puzzles kept per level, for starting a game offline.
const BUFFER_SIZE = 5;

export const DEFAULT_SETTINGS = { showMistakes: false, highlightSame: true, autoNotes: true };

/**
 * Calls the API. Resolves with { status, data } for any HTTP response;
 * rejects only when the server can't be reached.
 */
export async function api(method, path, body) {
  const opts = { method, headers: {}, signal: AbortSignal.timeout(15_000) };
  if (body !== undefined) {
    opts.headers["Content-Type"] = "application/json";
    opts.body = JSON.stringify(body);
  }
  const res = await fetch(path, opts);
  let data = null;
  try {
    data = await res.json();
  } catch {
    // 202, 204, or a non-JSON error page from the proxy.
  }
  return { status: res.status, data };
}

const errorText = (r) => r.data?.error ?? `The server answered ${r.status}.`;

// --- Puzzle buffer ---

// The buffer is read-modify-written by both takePuzzle and refill, across
// awaits. Chaining every change through one promise serialises them.
let bufferLock = Promise.resolve();
function withBuffer(fn) {
  const p = bufferLock.then(async () => {
    const buf = await db.getMeta("buffer", {});
    const out = await fn(buf);
    await db.setMeta("buffer", buf);
    return out;
  });
  bufferLock = p.catch(() => {});
  return p;
}

export async function bufferedCount(level) {
  const buf = await db.getMeta("buffer", {});
  return buf[level]?.length ?? 0;
}

/**
 * Returns a puzzle for level: from the buffer if it has one, else fresh
 * from the server. Throws when neither works.
 */
export async function takePuzzle(level) {
  const fromBuffer = await withBuffer((buf) => buf[level]?.shift());
  refill();
  if (fromBuffer) return fromBuffer;
  const r = await api("GET", `/api/puzzle?level=${level}`);
  if (r.status !== 200) throw new Error(errorText(r));
  return r.data;
}

let refilling = false;

/** Tops the buffer up to BUFFER_SIZE per level, one request at a time. */
export async function refill() {
  if (refilling) return;
  refilling = true;
  try {
    for (const level of LEVELS) {
      while ((await bufferedCount(level)) < BUFFER_SIZE) {
        const r = await api("GET", `/api/puzzle?level=${level}`);
        // Stop on any refusal, a 429 above all: the next refill is soon
        // enough, and retrying now only extends the rate limit.
        if (r.status !== 200) return;
        await withBuffer((buf) => (buf[level] ??= []).push(r.data));
      }
    }
  } catch {
    // Offline. The next refill tries again.
  } finally {
    refilling = false;
  }
}

// --- Account ---

export async function requestCode(email) {
  const r = await api("POST", "/api/auth/code", { email });
  if (r.status !== 202) throw new Error(errorText(r));
}

/** Verifies a code and makes this device that account's. */
export async function verifyCode(email, code) {
  const r = await api("POST", "/api/auth/verify", { email, code });
  if (r.status !== 200) throw new Error(errorText(r));
  const owner = await db.getMeta("owner", null);
  if (owner && owner !== r.data.email) {
    // Local games belong to a different account. They were synced to it,
    // so drop them rather than upload them into this one.
    await db.clearAccount();
  }
  await db.setMeta("account", r.data.email);
  await db.setMeta("owner", r.data.email);
  return r.data.email;
}

/**
 * Asks the server who is signed in. Updates the stored account, and
 * returns it. Offline, trusts the stored account.
 */
export async function checkAccount() {
  try {
    const r = await api("GET", "/api/me");
    if (r.status === 200) {
      await db.setMeta("account", r.data.email);
      return r.data.email;
    }
    if (r.status === 401) {
      // Session expired or revoked. Keep the games: signing in again as the
      // same address pushes anything unsynced.
      await db.setMeta("account", null);
      return null;
    }
  } catch {
    // Offline.
  }
  return db.getMeta("account", null);
}

/** Pushes what it can, then signs out and removes the account's data. */
export async function signOut() {
  try {
    await sync();
    await api("POST", "/api/auth/logout");
  } catch {
    // Offline: the cookie stays until it expires, but this device forgets.
  }
  await db.clearAccount();
}

// --- Settings ---

export async function getSettings() {
  const rec = await db.getMeta("settings", null);
  return { ...DEFAULT_SETTINGS, ...rec?.doc };
}

export async function saveSettings(doc) {
  const rec = await db.getMeta("settings", { rev: 0 });
  await db.setMeta("settings", { doc, rev: rec.rev, dirty: true });
  scheduleSync();
}

// --- Sync ---

let listeners = { gameReplaced: () => {}, settingsReplaced: () => {}, signedOut: () => {} };

/** Registers callbacks for changes sync makes underneath the app. */
export function onSync(l) {
  listeners = { ...listeners, ...l };
}

let running = null;
let again = false;

/**
 * Pushes local changes, then pulls the server's. One run at a time; a call
 * during a run queues one more run after it.
 */
export function sync() {
  if (running) {
    again = true;
    return running;
  }
  running = (async () => {
    try {
      do {
        again = false;
        await syncOnce();
      } while (again);
    } finally {
      running = null;
    }
  })();
  return running;
}

let timer;
/** Syncs a couple of seconds after the last change, so moves batch up. */
export function scheduleSync() {
  clearTimeout(timer);
  timer = setTimeout(() => sync().catch(() => {}), 2000);
}

async function syncOnce() {
  if (!(await db.getMeta("account", null))) return;

  for (const rec of await db.allGames()) {
    if (rec.dirty && !(await pushGame(rec))) return;
  }
  const settings = await db.getMeta("settings", null);
  if (settings?.dirty && !(await pushSettings(settings))) return;
  await pull();
}

async function unauthorized() {
  await db.setMeta("account", null);
  listeners.signedOut();
}

// pushGame returns false when sync should stop (signed out).
async function pushGame(rec) {
  const pushed = rec.doc;
  const r = await api("PUT", `/api/games/${rec.id}`, { base_rev: rec.rev, doc: pushed });
  if (r.status === 401) {
    await unauthorized();
    return false;
  }
  if (r.status === 200) {
    await db.updateGame(rec.id, (cur) => {
      if (!cur) return undefined;
      // Moves made while the request was in flight keep the record dirty.
      return { ...cur, rev: r.data.rev, dirty: cur.doc.updated !== pushed.updated };
    });
  } else if (r.status === 409) {
    await adoptServerGame(r.data.game, true);
  } else {
    throw new Error(errorText(r));
  }
  return true;
}

// adoptServerGame replaces the local copy. lost is true when the local copy
// had unsynced moves that this discards.
async function adoptServerGame(g, lost) {
  await db.putGame({ id: g.id, doc: JSON.parse(g.doc), rev: g.rev, dirty: false });
  listeners.gameReplaced(g.id, lost);
}

async function pushSettings(rec) {
  const r = await api("PUT", "/api/settings", { base_rev: rec.rev, doc: rec.doc });
  if (r.status === 401) {
    await unauthorized();
    return false;
  }
  if (r.status === 200) {
    const cur = await db.getMeta("settings", rec);
    await db.setMeta("settings", { ...cur, rev: r.data.rev, dirty: JSON.stringify(cur.doc) !== JSON.stringify(rec.doc) });
  } else if (r.status === 409) {
    await db.setMeta("settings", { doc: JSON.parse(r.data.settings.doc), rev: r.data.settings.rev, dirty: false });
    listeners.settingsReplaced();
  } else {
    throw new Error(errorText(r));
  }
  return true;
}

async function pull() {
  const since = await db.getMeta("cursor", 0);
  const r = await api("GET", `/api/sync?since=${since}`);
  if (r.status === 401) return unauthorized();
  if (r.status !== 200) throw new Error(errorText(r));

  for (const g of r.data.games) {
    const local = await db.getGame(g.id);
    if (local && local.rev >= g.rev) continue; // this device's own write, or newer
    await adoptServerGame(g, Boolean(local?.dirty));
  }
  if (r.data.settings) {
    const local = await db.getMeta("settings", null);
    if (!local || local.rev < r.data.settings.rev) {
      await db.setMeta("settings", { doc: JSON.parse(r.data.settings.doc), rev: r.data.settings.rev, dirty: false });
      listeners.settingsReplaced();
    }
  }
  await db.setMeta("cursor", r.data.cursor);
}
