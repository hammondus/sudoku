# Design decisions

This file records choices where a reasonable alternative existed, and why
this project took the route it did.

## Difficulty is graded by technique, not clue count

A puzzle's level is the hardest human technique the grader needs to solve
it (`internal/sudoku/grade.go`). Clue count tracks difficulty only loosely: a
24-clue puzzle can fall to singles, and a 30-clue puzzle can need an X-Wing.

| Level | Techniques |
|---|---|
| Easy | Hidden single, naked single |
| Medium | Pointing pair, box/line reduction, naked and hidden pairs and triples |
| Hard | X-Wing, XY-Wing, naked quad, Swordfish |
| Expert | XYZ-Wing, simple colouring |

The grader never guesses. A puzzle its techniques can't finish grades
`TooHard`, and the generator discards it.

Easy and Medium also have clue floors (36 and 30), because players read a
sparse grid as hard whatever technique it needs. Hard and Expert have none.

## Hidden quad and Jellyfish are not implemented

Both were written and then removed. In 5,000 random puzzles neither ever
fired, and the reason is structural:

- A hidden quad in a house with eight or fewer empty cells is the
  complement of a naked subset of four or fewer cells. The grader tries
  naked subsets first.
- A Jellyfish's complement is a smaller fish in the other orientation, which
  the grader tries first.

Code that never runs can't be tested, so the techniques went. Expert rests
on XYZ-Wing and simple colouring, which together produce Expert puzzles in
about 6% of fully dug random grids.

## The generator grades while it digs

`Generate` empties cells one at a time and keeps a removal only if the
solution stays unique and the grade stays at or below the target. Grading
only at the end would dig straight past Hard and Expert into puzzles the
grader can't rate. Measured cost per puzzle: about 1 ms for Easy and about
25 ms for the other levels on an Apple M-series core.

## Soundness is the main engine test

`TestSoundRandomDigs` grades 1,000 random minimal puzzles and replays every
step against the true solution. A step that places a wrong digit or removes
the right one fails the test. The test also fails if any technique never
fires, so a technique can't sit untested.

## Puzzles are generated on the server, and the client keeps a buffer

Generation runs in Go, where the grader is testable. A PWA should play
offline, so the client keeps 5 unplayed puzzles per level in IndexedDB and
refills them while online. Offline play is limited to that buffer: 20 new
games.

Alternatives considered:

- Generating in the browser (JavaScript) gives unlimited offline games, but
  it means a second implementation of the solver and grader.
- Compiling the Go engine to WebAssembly gives one codebase, but it costs a
  download of about 2 MB, or a TinyGo toolchain.

## Each puzzle carries its solution and solving path

The client needs the solution to flag mistakes offline, and the grader's
step list to give hints offline. Shipping the solution lets a player cheat
with developer tools. This is a single-player game, so that cost is accepted.

## Hints: live on the server, from the path offline

`POST /api/hint` takes the givens and the current board and returns the
easiest next step from that exact position. The endpoint is stateless, so
guests can use it.

Offline, `web/js/hint.js` walks the stored solving path and returns the
first placement whose cell is still empty, with the eliminations that lead
to it. The offline hint is always correct, but it follows the grader's order,
not the player's, so it isn't always the easiest move on the board.

A hint never fills a cell; it explains the technique. If the board has a
wrong digit, the hint reports the mistake instead, because a deduction from a
wrong board would be wrong too.

## Sign-in by emailed code, invite-only

- **Code, not link.** On iOS a link in Mail opens Safari, not the installed
  PWA, so the session would land in the wrong browser.
- **Invite-only.** An account row is the allowlist. Uninvited addresses get
  the same 202 response and no email, so the endpoint can't be used to find
  out who has an account or to send mail to strangers. The email goes out
  after the response, so timing doesn't reveal it either.
- **Limits.** One live code per account. A code expires after 10 minutes,
  and dies after 5 wrong guesses. Resends are limited to one a minute per
  account, and code requests to 5 at once, then one every 30 seconds, per
  address. Wrong guesses lock an address out for 15 minutes after 10.
- **Sessions** last a year, fixed. The database stores a SHA-256 of the
  session token, so a copied database doesn't yield working cookies.
- **CSRF.** The session cookie is `SameSite=Lax`, and every write is JSON.
  A cross-site form can't send JSON, and a cross-site `fetch` that could
  would need a CORS preflight that the server never answers.

Email goes through `github.com/hammondus/mailer`, configured from
`SUDOKU_SMTP_*`. `airport-timezone` has its own copy of a mailer package in
`internal/mailer`; this project uses the shared module instead.

## Sync: revision check, first writer wins

Each game is an opaque JSON document the client owns. The server stores it
with a revision number. A push names the revision it was based on; a
mismatch gets a 409 with the server's copy, and the client adopts it and
tells the player. The losing device loses its unsynced moves on that game.

This was chosen over keeping both copies, which avoids losing moves but fills
the game list with duplicates.

Pulls use a per-account change counter (`users.seq`): every write takes the
next value, so "what changed since N" is one indexed query.

Guests play with games stored on the device only. Signing in uploads them,
because every guest game is marked dirty with revision 0. Signing in as a
different account than the one the device's games belong to clears them
first, so one account's games never upload into another.

## Frontend: vanilla ES modules, no htmx, no build step

The game is one screen of client-side state that has to work offline.
htmx swaps server-rendered fragments, which is the wrong shape for it, and
Svelte would add a build step for one grid and a few dialogs.

## Every static file goes through nitrokit.Assets

The shell loads `app.js` and `style.css` by content-hashed URLs, which are
immutable for a year. Everything else is fetched by a plain URL: the ES
modules that `app.js` imports (`./game.js`), `sw.js`, the manifest, and its
icons. From v0.5.0, `nitrokit.Assets` serves a plain URL as `no-cache` with
an `ETag`, so after a deploy a new `app.js` never runs against an old
`game.js`, and an unchanged file costs a 304.

`sw.js` and the manifest used to go through `http.FileServerFS`, which sends
no `ETag` for embedded files, so `no-cache` there would mean a full transfer
on every check. Routing them through `Assets` gives them the same policy and
validator as everything else, and leaves one static handler.

## Dark mode uses a media query, not light-dark()

Colour tokens switch under `prefers-color-scheme`, which has been widely
supported for years. `light-dark()` has been Baseline only since 2024.

## The `hidden` attribute always wins

`[hidden] { display: none !important; }` is in the stylesheet because a
class that sets `display` overrides the browser's own `hidden` rule. Before
this rule, the update banner showed on every load.

## Grid navigation is index-based

The general guidance for arrow-key focus is to choose targets from live
geometry. A sudoku grid is always 9 × 9, so the next cell is index ± 1 or
± 9, which is simpler and exact. The grid keeps one tab stop with a roving
`tabindex`, and an arrow key at an edge is left alone so the page can
scroll.

## JSON uses encoding/json, not encoding/json/v2

Go 1.27 recommends `encoding/json/v2` for new code. The request and response
helpers come from `nitrokit` (`ReadJSON`, `WriteJSON`), which use v1, so the
project stays on v1 rather than mixing the two.
