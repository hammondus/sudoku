# Sudoku

A sudoku PWA with four graded levels, hints that explain the technique, and
sync between devices. A Go server generates puzzles and handles sign-in and
sync; the frontend is vanilla JavaScript with no build step.

For why things are built the way they are, see
[DESIGN-DECISIONS.md](DESIGN-DECISIONS.md).

## Layout

| Path | Contents |
|---|---|
| `internal/sudoku/` | Solver, generator, grader, and hints. No HTTP. |
| `main.go` | Flags, account commands, routes |
| `auth.go` | Emailed-code sign-in and sessions |
| `api.go` | Puzzle, hint, and sync endpoints |
| `store.go` | SQLite schema and queries |
| `web/` | The PWA, embedded into the binary at build time |

## Run locally

1. To create an account, run `make invite EMAIL=you@example.com`.
2. To start the server, run `make run`, then open `http://localhost:8080`.
3. To sign in, enter your address. The server prints the code in its log,
   because SMTP isn't configured in development.

## Deploy

1. On the server, copy `.env.example` to `.env` and fill in the SES SMTP
   settings.
2. Run `make deploy`.
3. In Nginx Proxy Manager, point the host at `sudoku:8080`.
4. To invite a player, run
   `docker compose exec sudoku /app/sudoku -invite player@example.com`.

To release a new version, bump `VERSION`. The service worker names its
cache after it, and open clients show a banner offering a reload.

## Account commands

| Command | Effect |
|---|---|
| `-invite EMAIL` | Creates an account. Only invited addresses receive codes. |
| `-revoke EMAIL` | Deletes the account, its sessions, and its games. |
| `-users` | Lists accounts. |

## Keyboard

| Key | Action |
|---|---|
| Arrow keys | Move the selection |
| 1–9 | Enter a digit, or a note in notes mode |
| Shift+1–9 | Toggle a note |
| Backspace, Delete, 0 | Erase |
| N | Toggle notes mode |
| Ctrl+Z or ⌘Z | Undo |
