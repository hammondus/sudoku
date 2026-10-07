// UI controller: builds the grid, routes input to the game model, runs the
// timer, and drives dialogs, hints, and sync. All game rules live in
// game.js; all storage and network in db.js and sync.js.

import { APP_VERSION } from "./version.js";
import * as G from "./game.js";
import * as db from "./db.js";
import * as S from "./sync.js";
import { offlineHint } from "./hint.js";

const $ = (id) => document.getElementById(id);

const state = {
  game: null, // the game document in play, or null
  sel: 40, // selected cell
  notesMode: false,
  settings: { ...S.DEFAULT_SETTINGS },
  account: null, // signed-in email, or null for a guest
  paused: false,
  checked: null, // Set of cells the Check button flagged; cleared by the next move
  hint: null, // { mistakes } or { steps } from the last hint
  hintStep: 0, // which hint step is highlighted
};

// --- Grid ---

const cells = [];

function buildGrid() {
  const grid = $("grid");
  for (let r = 0; r < 9; r++) {
    const rowEl = document.createElement("div");
    rowEl.setAttribute("role", "row");
    rowEl.className = "grid-row";
    for (let c = 0; c < 9; c++) {
      const i = r * 9 + c;
      const el = document.createElement("div");
      el.className = "cell";
      if (c % 3 === 2) el.classList.add("box-r");
      if (r % 3 === 2) el.classList.add("box-b");
      if (c === 8) el.classList.add("last-c");
      if (r === 8) el.classList.add("last-r");
      el.setAttribute("role", "gridcell");
      el.tabIndex = i === state.sel ? 0 : -1;
      el.dataset.cell = String(i);
      rowEl.append(el);
      cells.push(el);
    }
    grid.append(rowEl);
  }
  grid.addEventListener("click", (e) => {
    const el = e.target.closest(".cell");
    if (el) select(Number(el.dataset.cell), true);
  });
}

function buildPad() {
  const pad = $("pad");
  for (let d = 1; d <= 9; d++) {
    const b = document.createElement("button");
    b.type = "button";
    b.dataset.digit = String(d);
    b.innerHTML = `${d}<small></small>`;
    b.addEventListener("click", () => input(d));
    pad.append(b);
  }
}

function select(i, focus) {
  cells[state.sel].tabIndex = -1;
  state.sel = i;
  cells[i].tabIndex = 0;
  if (focus) cells[i].focus();
  render();
}

// --- Rendering ---

function hintClasses() {
  const out = new Map(); // cell → class
  const elims = new Map(); // cell → Set of digits struck through in notes
  const h = state.hint;
  if (!h) return { out, elims };
  if (h.mistakes) {
    for (const c of h.mistakes) out.set(c, "wrong");
    return { out, elims };
  }
  const step = h.steps?.[state.hintStep];
  if (!step) return { out, elims };
  for (const u of step.units ?? []) for (const c of G.unitCells(u)) out.set(c, "hint-unit");
  for (const c of step.cells ?? []) out.set(c, "hint-a");
  for (const c of step.alt ?? []) out.set(c, "hint-b");
  for (const e of step.eliminations ?? []) {
    out.set(e.cell, "hint-elim");
    if (!elims.has(e.cell)) elims.set(e.cell, new Set());
    elims.get(e.cell).add(e.digit);
  }
  if (step.place) out.set(step.place.cell, "hint-target");
  return { out, elims };
}

function render() {
  const g = state.game;
  document.body.classList.toggle("notes-mode", state.notesMode);
  $("notes-btn").setAttribute("aria-pressed", String(state.notesMode));
  $("check-btn").hidden = state.settings.showMistakes;
  $("empty").hidden = Boolean(g);
  $("paused").hidden = !(g && state.paused);
  $("grid").setAttribute("aria-hidden", String(!g || state.paused));
  $("level-label").textContent = g ? g.level : "Sudoku";
  renderTimer();

  const playing = g?.status === "playing";
  for (const id of ["erase-btn", "notes-btn", "hint-btn", "check-btn"]) $(id).disabled = !playing;
  $("undo-btn").disabled = !playing || g.history.length === 0;
  $("pause-btn").disabled = !playing;

  const counts = g ? G.digitCounts(g) : new Array(10).fill(0);
  for (const b of $("pad").children) {
    const d = Number(b.dataset.digit);
    b.disabled = !playing || counts[d] >= 9;
    b.querySelector("small").textContent = g ? String(9 - counts[d]) : "";
    b.setAttribute("aria-label", `${d}, ${9 - counts[d]} left`);
  }

  if (!g) {
    for (const el of cells) {
      el.className = el.className.split(" ").filter((k) => k === "cell" || k.startsWith("box-") || k.startsWith("last-")).join(" ");
      el.textContent = "";
    }
    return;
  }

  const sel = state.sel;
  const selDigit = g.entries[sel];
  const wrong = new Set(state.settings.showMistakes ? G.wrongCells(g) : (state.checked ?? []));
  // Repeated digits break the rules where anyone can see them, so they show
  // whatever the mistakes setting says.
  const clash = G.clashCells(g);
  const { out: hintCls, elims } = hintClasses();

  for (let c = 0; c < 81; c++) {
    const el = cells[c];
    const v = g.entries[c];
    const given = G.isGiven(g, c);
    const k = el.classList;
    k.toggle("given", given);
    k.toggle("selected", c === sel);
    k.toggle("peer", c !== sel && G.peers[sel].includes(c));
    k.toggle("same", state.settings.highlightSame && selDigit !== 0 && v === selDigit && c !== sel);
    k.toggle("wrong", wrong.has(c) || hintCls.get(c) === "wrong");
    k.toggle("clash", clash.has(c));
    for (const h of ["hint-a", "hint-b", "hint-elim", "hint-target", "hint-unit"]) k.toggle(h, hintCls.get(c) === h);

    if (v) {
      el.textContent = String(v);
    } else if (g.notes[c]) {
      let html = '<div class="notes">';
      for (let d = 1; d <= 9; d++) {
        const on = g.notes[c] & (1 << d);
        let cls = "";
        if (on && elims.get(c)?.has(d)) cls = "elim";
        else if (on && state.settings.highlightSame && d === selDigit) cls = "on-same";
        html += `<span class="${cls}">${on ? d : ""}</span>`;
      }
      el.innerHTML = html + "</div>";
    } else {
      el.textContent = "";
    }

    let label = `Row ${G.row(c) + 1}, column ${G.col(c) + 1}: `;
    if (v) label += given ? `${v}, given` : String(v);
    else label += "empty";
    if (!v && g.notes[c]) {
      const ds = [];
      for (let d = 1; d <= 9; d++) if (g.notes[c] & (1 << d)) ds.push(d);
      label += `, notes ${ds.join(" ")}`;
    }
    if (wrong.has(c)) label += ", wrong";
    if (clash.has(c)) label += ", repeated in its row, column, or box";
    el.setAttribute("aria-label", label);
  }
}

function renderTimer() {
  $("timer").textContent = G.formatTime(state.game?.elapsed ?? 0);
}

// --- Moves ---

function input(d) {
  const g = state.game;
  if (!g || state.paused) return;
  const changed = state.notesMode ? G.toggleNote(g, state.sel, d) : G.setDigit(g, state.sel, d, state.settings.autoNotes);
  if (changed) afterMove();
}

function afterMove() {
  state.checked = null;
  closeHint();
  const g = state.game;
  if (G.isSolved(g)) {
    g.status = "won";
    g.finished = Date.now();
    g.updated = g.finished;
  }
  render();
  save();
  if (g.status === "won") showWin();
}

function erase() {
  if (state.game && !state.paused && G.erase(state.game, state.sel)) afterMove();
}

function undo() {
  if (!state.game || state.paused) return;
  const c = G.undo(state.game);
  if (c === null) return;
  select(c, false);
  afterMove();
}

function check() {
  const g = state.game;
  if (!g || g.status !== "playing") return;
  g.checks++;
  const wrong = G.wrongCells(g);
  state.checked = new Set(wrong);
  toast(wrong.length === 0 ? "No mistakes so far." : `${wrong.length} wrong ${wrong.length === 1 ? "digit is" : "digits are"} marked in red.`);
  render();
  save();
}

// --- Hints ---

async function hint() {
  const g = state.game;
  if (!g || g.status !== "playing" || state.paused) return;
  let h;
  try {
    const r = await S.api("POST", "/api/hint", { givens: g.givens, board: G.board(g) });
    if (r.status !== 200) throw new Error();
    h = r.data;
  } catch {
    h = offlineHint(g);
  }
  g.hints++;
  save();
  showHint(h);
}

function showHint(h) {
  state.hint = h;
  state.hintStep = 0;
  const list = $("hint-steps");
  list.replaceChildren();
  if (h.mistakes?.length) {
    $("hint-title").textContent = "Fix mistakes first";
    const n = h.mistakes.length;
    addHintItem(list, `${n} ${n === 1 ? "digit is" : "digits are"} wrong, marked in red. A hint from a wrong board would be wrong too.`);
  } else if (!h.steps?.length) {
    $("hint-title").textContent = "No hint";
    addHintItem(list, "No technique this app knows applies here. Check your digits.");
  } else {
    const techniques = [...new Set(h.steps.map((s) => s.technique))];
    $("hint-title").textContent = `Hint: ${techniques.join(", then ")}`;
    h.steps.forEach((s, i) => {
      const li = addHintItem(list, s.text, s.technique);
      li.dataset.step = String(i);
      if (h.steps.length > 1) {
        li.tabIndex = 0;
        li.addEventListener("click", () => highlightStep(i));
        li.addEventListener("keydown", (e) => {
          if (e.key === "Enter" || e.key === " ") {
            e.preventDefault();
            highlightStep(i);
          }
        });
      }
    });
  }
  $("hint-panel").hidden = false;
  highlightStep(0);
}

function addHintItem(list, text, technique) {
  const li = document.createElement("li");
  if (technique) {
    const b = document.createElement("strong");
    b.textContent = `${technique}. `;
    li.append(b);
  }
  li.append(text);
  list.append(li);
  return li;
}

function highlightStep(i) {
  state.hintStep = i;
  for (const li of $("hint-steps").children) li.classList.toggle("active", li.dataset.step === String(i));
  render();
}

function closeHint() {
  if (!state.hint) return;
  state.hint = null;
  $("hint-panel").hidden = true;
}

// --- Timer ---

let lastTick = performance.now();
let sinceSave = 0;

function tick() {
  const now = performance.now();
  const dt = now - lastTick;
  lastTick = now;
  const g = state.game;
  // An open dialog covers the board, so it stops the clock like a pause.
  if (!g || g.status !== "playing" || state.paused || document.visibilityState !== "visible") return;
  if (document.querySelector("dialog[open]")) return;
  g.elapsed += dt;
  renderTimer();
  sinceSave += dt;
  if (sinceSave > 15_000) save({ sync: false });
}

function setPaused(p) {
  state.paused = p;
  render();
  if (!p) cells[state.sel].focus();
}

// --- Persistence ---

async function save({ sync = true } = {}) {
  const g = state.game;
  if (!g) return;
  sinceSave = 0;
  await db.updateGame(g.id, (cur) => {
    // Sync may have just adopted the server's newer copy, and the reload
    // into state.game is still in flight. Writing this older copy over it
    // with the new revision would push stale moves as if they were current.
    if (cur && !cur.dirty && cur.doc.updated > g.updated) return undefined;
    return { id: g.id, doc: g, rev: cur?.rev ?? 0, dirty: true };
  });
  await db.setMeta("current", g.id);
  if (sync) S.scheduleSync();
}

async function loadGame(id) {
  const rec = id && (await db.getGame(id));
  state.game = rec ? rec.doc : null;
  state.checked = null;
  state.paused = false;
  closeHint();
  if (state.game) await db.setMeta("current", id);
  render();
}

// --- New game, resume, win ---

async function startGame(level) {
  closeDialogs();
  try {
    const p = await S.takePuzzle(level);
    state.game = G.newGame(p);
  } catch (e) {
    const offline = !navigator.onLine || e instanceof TypeError;
    toast(offline ? `You're offline, and no ${level} puzzles are saved on this device. Connect to get more.` : e.message);
    return;
  }
  state.checked = null;
  state.paused = false;
  state.sel = 40;
  closeHint();
  await save();
  select(40, true);
}

async function fillResumeList() {
  const games = (await db.allGames())
    .map((r) => r.doc)
    .filter((g) => g.status === "playing" && g.id !== state.game?.id)
    .sort((a, b) => b.updated - a.updated);
  const list = $("resume-list");
  list.replaceChildren();
  for (const g of games.slice(0, 10)) {
    const filled = g.entries.filter((v) => v).length;
    const li = document.createElement("li");
    const b = document.createElement("button");
    b.type = "button";
    const name = document.createElement("span");
    name.textContent = g.level[0].toUpperCase() + g.level.slice(1);
    const meta = document.createElement("span");
    meta.className = "meta";
    meta.textContent = `${filled}/81 · ${G.formatTime(g.elapsed)}`;
    b.append(name, meta);
    b.addEventListener("click", async () => {
      closeDialogs();
      await loadGame(g.id);
    });
    li.append(b);
    list.append(li);
  }
  $("resume-section").hidden = games.length === 0;
}

function showWin() {
  const g = state.game;
  const hints = g.hints ? ` with ${g.hints} ${g.hints === 1 ? "hint" : "hints"}` : "";
  $("win-text").textContent = `You solved this ${g.level} puzzle in ${G.formatTime(g.elapsed)}${hints}.`;
  $("win-dlg").showModal();
}

async function fillStats() {
  const games = (await db.allGames()).map((r) => r.doc);
  const body = $("stats-body");
  body.replaceChildren();
  for (const level of G.LEVELS) {
    const won = games.filter((g) => g.level === level && g.status === "won");
    const clean = won.filter((g) => !g.hints).map((g) => g.elapsed);
    const best = clean.length ? G.formatTime(Math.min(...clean)) : "–";
    const avg = clean.length ? G.formatTime(clean.reduce((a, b) => a + b, 0) / clean.length) : "–";
    const tr = document.createElement("tr");
    for (const v of [level, String(won.length), best, avg]) {
      const td = document.createElement("td");
      td.textContent = v;
      tr.append(td);
    }
    body.append(tr);
  }
}

// --- Settings ---

function fillSettings() {
  $("set-mistakes").checked = state.settings.showMistakes;
  $("set-same").checked = state.settings.highlightSame;
  $("set-autonotes").checked = state.settings.autoNotes;
}

function bindSettings() {
  const bind = (id, key) =>
    $(id).addEventListener("change", (e) => {
      state.settings = { ...state.settings, [key]: e.target.checked };
      S.saveSettings(state.settings);
      render();
    });
  bind("set-mistakes", "showMistakes");
  bind("set-same", "highlightSame");
  bind("set-autonotes", "autoNotes");
}

// --- Account ---

let pendingEmail = "";

function fillAccount() {
  const signedIn = Boolean(state.account);
  $("acct-guest").hidden = signedIn;
  $("acct-user").hidden = !signedIn;
  $("acct-email").textContent = state.account ?? "";
  $("acct-error").textContent = "";
  $("acct-sync").textContent = "Signing out removes your games from this device. They stay in your account.";
  $("menu-account").textContent = signedIn ? `Signed in as ${state.account}` : "Playing as a guest. Games stay on this device.";
}

function bindAccount() {
  $("email-form").addEventListener("submit", async (e) => {
    e.preventDefault();
    $("acct-error").textContent = "";
    pendingEmail = $("email-input").value.trim();
    try {
      await S.requestCode(pendingEmail);
    } catch (err) {
      $("acct-error").textContent = err instanceof TypeError ? "You're offline. Connect, then try again." : err.message;
      return;
    }
    $("code-sent").textContent = `If ${pendingEmail} has an invitation, a code is on its way. It expires in 10 minutes.`;
    $("email-form").hidden = true;
    $("code-form").hidden = false;
    $("code-input").value = "";
    $("code-input").focus();
  });
  $("code-back").addEventListener("click", () => {
    $("code-form").hidden = true;
    $("email-form").hidden = false;
    $("acct-error").textContent = "";
  });
  $("code-form").addEventListener("submit", async (e) => {
    e.preventDefault();
    $("acct-error").textContent = "";
    try {
      state.account = await S.verifyCode(pendingEmail, $("code-input").value.trim());
    } catch (err) {
      $("acct-error").textContent = err instanceof TypeError ? "You're offline. Connect, then try again." : err.message;
      return;
    }
    $("code-form").hidden = true;
    $("email-form").hidden = false;
    fillAccount();
    toast(`Signed in as ${state.account}.`);
    await runSync();
  });
  $("signout-btn").addEventListener("click", async () => {
    await S.signOut();
    state.account = null;
    state.game = null;
    state.settings = await S.getSettings();
    closeDialogs();
    fillAccount();
    render();
    toast("Signed out.");
  });
}

async function runSync() {
  try {
    await S.sync();
  } catch {
    // Offline or server error: the next trigger retries.
  }
  if (!state.game) {
    // A first sync on a new device may have brought games in.
    const id = await db.getMeta("current", null);
    if (id) await loadGame(id);
  }
}

// --- Dialogs and toast ---

function closeDialogs() {
  for (const d of document.querySelectorAll("dialog[open]")) d.close();
}

function openDialog(id) {
  closeDialogs();
  if (id === "new-dlg") fillResumeList();
  if (id === "stats-dlg") fillStats();
  if (id === "settings-dlg") fillSettings();
  if (id === "account-dlg") fillAccount();
  $(id).showModal();
}

let toastTimer;
function toast(msg) {
  const t = $("toast");
  t.textContent = msg;
  t.classList.add("show");
  clearTimeout(toastTimer);
  toastTimer = setTimeout(() => t.classList.remove("show"), 4000);
}

// --- Keyboard ---

function onKey(e) {
  if (document.querySelector("dialog[open]")) return;
  if (e.target.matches?.("input, textarea, select")) return;
  if (!state.game || state.paused) return;

  if ((e.ctrlKey || e.metaKey) && e.key.toLowerCase() === "z") {
    e.preventDefault();
    undo();
    return;
  }
  if (e.ctrlKey || e.metaKey || e.altKey) return;

  // e.code, not e.key: Shift+1 is "!" as a key but still Digit1 as a code,
  // and Shift+digit is the notes shortcut.
  const m = /^(?:Digit|Numpad)([1-9])$/.exec(e.code);
  if (m) {
    e.preventDefault();
    const d = Number(m[1]);
    if (e.shiftKey && !state.notesMode) {
      if (G.toggleNote(state.game, state.sel, d)) afterMove();
    } else {
      input(d);
    }
    return;
  }

  const moves = { ArrowUp: -9, ArrowDown: 9, ArrowLeft: -1, ArrowRight: 1 };
  if (e.key in moves && e.target.closest?.("#grid")) {
    const c = state.sel;
    const next = c + moves[e.key];
    const sameRow = e.key === "ArrowLeft" || e.key === "ArrowRight" ? G.row(next) === G.row(c) : true;
    // At an edge, leave the key alone so the page can scroll.
    if (next < 0 || next > 80 || !sameRow) return;
    e.preventDefault();
    select(next, true);
    return;
  }

  switch (e.key) {
    case "Backspace":
    case "Delete":
    case "0":
      e.preventDefault();
      erase();
      break;
    case "n":
    case "N":
      state.notesMode = !state.notesMode;
      render();
      break;
  }
}

// --- Version and service worker ---

async function checkVersion() {
  try {
    const res = await fetch("/js/version.js", { cache: "no-store" });
    const m = /APP_VERSION = "([^"]+)"/.exec(await res.text());
    if (m && m[1] !== APP_VERSION) $("update-banner").hidden = false;
  } catch {
    // Offline.
  }
}

function registerWorker() {
  if (!("serviceWorker" in navigator)) return;
  // The version in the URL makes a release a new worker, which names a new
  // cache. See sw.js.
  navigator.serviceWorker.register(`/sw.js?v=${encodeURIComponent(APP_VERSION)}`).catch((e) => {
    console.warn("service worker registration failed", e);
  });
}

// --- Start ---

async function start() {
  buildGrid();
  buildPad();
  bindSettings();
  bindAccount();

  $("menu-btn").addEventListener("click", () => openDialog("menu-dlg"));
  for (const b of document.querySelectorAll("[data-open]")) b.addEventListener("click", () => openDialog(b.dataset.open));
  for (const b of document.querySelectorAll("[data-level]")) b.addEventListener("click", () => startGame(b.dataset.level));
  $("empty-new-btn").addEventListener("click", () => openDialog("new-dlg"));
  $("undo-btn").addEventListener("click", undo);
  $("erase-btn").addEventListener("click", erase);
  $("notes-btn").addEventListener("click", () => {
    state.notesMode = !state.notesMode;
    render();
  });
  $("hint-btn").addEventListener("click", hint);
  $("check-btn").addEventListener("click", check);
  $("hint-close").addEventListener("click", () => {
    closeHint();
    render();
  });
  $("pause-btn").addEventListener("click", () => setPaused(true));
  $("resume-btn").addEventListener("click", () => setPaused(false));
  $("reload-btn").addEventListener("click", () => location.reload());
  $("win-dlg").addEventListener("close", () => {
    if ($("win-dlg").returnValue === "new") openDialog("new-dlg");
  });
  document.addEventListener("keydown", onKey);

  document.addEventListener("visibilitychange", () => {
    if (document.visibilityState === "hidden") {
      save({ sync: false }).then(() => S.sync().catch(() => {}));
    } else {
      lastTick = performance.now();
      runSync();
      checkVersion();
    }
  });
  window.addEventListener("online", () => {
    runSync();
    S.refill();
  });

  S.onSync({
    gameReplaced: async (id, lost) => {
      if (state.game?.id !== id) return;
      await loadGame(id);
      if (lost) toast("Another device saved this game first. You're now seeing that version.");
    },
    settingsReplaced: async () => {
      state.settings = await S.getSettings();
      render();
    },
    signedOut: () => {
      state.account = null;
      toast("Your session ended. Sign in again to keep syncing.");
    },
  });

  state.settings = await S.getSettings();
  await loadGame(await db.getMeta("current", null));
  if (state.game?.status !== "playing") {
    $("empty-text").textContent = "Choose a level to start.";
  }
  setInterval(tick, 1000);

  registerWorker();
  state.account = await S.checkAccount();
  fillAccount();
  await runSync();
  S.refill();
  checkVersion();
}

start();
