// IndexedDB storage. Two object stores:
//
//   games: { id, doc, rev, dirty }
//     doc   the game document (game.js)
//     rev   the server revision this copy is based on; 0 = never synced
//     dirty true when the local copy has changes the server hasn't seen
//
//   meta: { key, value } for everything else: settings, the account email,
//     the sync cursor, and the buffer of unplayed puzzles.
//
// IndexedDB rather than localStorage: game documents are a few kilobytes
// each and accumulate, and localStorage is synchronous and small.

const DB_NAME = "sudoku";
const DB_VERSION = 1;

let dbPromise;

function open() {
  dbPromise ??= new Promise((resolve, reject) => {
    const req = indexedDB.open(DB_NAME, DB_VERSION);
    req.onupgradeneeded = () => {
      const db = req.result;
      db.createObjectStore("games", { keyPath: "id" });
      db.createObjectStore("meta", { keyPath: "key" });
    };
    req.onsuccess = () => resolve(req.result);
    req.onerror = () => reject(req.error);
  });
  return dbPromise;
}

/** Runs fn against one store and resolves with the request's result. */
async function run(store, mode, fn) {
  const db = await open();
  return new Promise((resolve, reject) => {
    const tx = db.transaction(store, mode);
    const req = fn(tx.objectStore(store));
    tx.oncomplete = () => resolve(req?.result);
    tx.onerror = () => reject(tx.error);
    tx.onabort = () => reject(tx.error);
  });
}

/**
 * Reads, changes, and writes one game record in a single transaction, so
 * the app saving a move and sync recording a new revision can't overwrite
 * each other's fields. fn gets the record (or undefined) and returns the
 * record to store, or undefined to leave it alone. fn must not await.
 */
export async function updateGame(id, fn) {
  const db = await open();
  return new Promise((resolve, reject) => {
    const tx = db.transaction("games", "readwrite");
    const store = tx.objectStore("games");
    let result;
    store.get(id).onsuccess = (e) => {
      result = fn(e.target.result);
      if (result !== undefined) store.put(result);
    };
    tx.oncomplete = () => resolve(result);
    tx.onerror = () => reject(tx.error);
    tx.onabort = () => reject(tx.error);
  });
}

export const getGame = (id) => run("games", "readonly", (s) => s.get(id));
export const putGame = (rec) => run("games", "readwrite", (s) => s.put(rec));
export const allGames = () => run("games", "readonly", (s) => s.getAll());

export async function getMeta(key, fallback) {
  const rec = await run("meta", "readonly", (s) => s.get(key));
  return rec === undefined ? fallback : rec.value;
}

export const setMeta = (key, value) => run("meta", "readwrite", (s) => s.put({ key, value }));

/**
 * Removes everything that belongs to an account: games, settings, the
 * sync cursor, and the account itself. The puzzle buffer stays; it belongs
 * to the device, not the player.
 */
export async function clearAccount() {
  await run("games", "readwrite", (s) => s.clear());
  for (const key of ["account", "owner", "cursor", "settings", "current"]) {
    await run("meta", "readwrite", (s) => s.delete(key));
  }
}
