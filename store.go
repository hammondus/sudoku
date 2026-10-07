package main

import (
	"context"
	"crypto/sha256"
	"crypto/subtle"
	"database/sql"
	"errors"
	"fmt"
	"strings"
	"time"

	_ "modernc.org/sqlite"
)

// store is the server's SQLite database: accounts, sign-in codes, sessions,
// and each account's synced games and settings. Guests have no rows here;
// their games live only in the browser until they sign in.
type store struct{ db *sql.DB }

const schema = `
CREATE TABLE IF NOT EXISTS users (
	id         INTEGER PRIMARY KEY,
	email      TEXT NOT NULL UNIQUE COLLATE NOCASE,
	created_at INTEGER NOT NULL,
	-- seq is the account's change counter. Every game or settings write
	-- takes the next value, so "changes since N" is one indexed query.
	seq          INTEGER NOT NULL DEFAULT 0,
	settings     TEXT    NOT NULL DEFAULT '{}',
	settings_rev INTEGER NOT NULL DEFAULT 0,
	settings_seq INTEGER NOT NULL DEFAULT 0
) STRICT;

CREATE TABLE IF NOT EXISTS login_codes (
	user_id    INTEGER PRIMARY KEY REFERENCES users(id) ON DELETE CASCADE,
	code_hash  BLOB    NOT NULL,
	expires_at INTEGER NOT NULL,
	attempts   INTEGER NOT NULL DEFAULT 0,
	sent_at    INTEGER NOT NULL
) STRICT;

CREATE TABLE IF NOT EXISTS sessions (
	token_hash BLOB PRIMARY KEY,
	user_id    INTEGER NOT NULL REFERENCES users(id) ON DELETE CASCADE,
	expires_at INTEGER NOT NULL
) STRICT;

CREATE TABLE IF NOT EXISTS games (
	user_id    INTEGER NOT NULL REFERENCES users(id) ON DELETE CASCADE,
	id         TEXT    NOT NULL,
	rev        INTEGER NOT NULL,
	seq        INTEGER NOT NULL,
	-- The game itself is an opaque JSON document the client owns: givens,
	-- solution, entries, pencil marks, timer, status. The server only
	-- versions it.
	doc        TEXT    NOT NULL,
	updated_at INTEGER NOT NULL,
	PRIMARY KEY (user_id, id)
) STRICT;

CREATE INDEX IF NOT EXISTS games_by_seq ON games (user_id, seq);
`

func openStore(path string) (*store, error) {
	// busy_timeout covers the odd overlap with a backup reader; foreign_keys
	// makes ON DELETE CASCADE real; WAL lets reads run during a write.
	dsn := "file:" + path + "?_pragma=busy_timeout(5000)&_pragma=foreign_keys(1)&_pragma=journal_mode(WAL)"
	db, err := sql.Open("sqlite", dsn)
	if err != nil {
		return nil, err
	}
	// One connection: SQLite has one writer anyway, and serialising here
	// avoids "database is locked" under bursty load.
	db.SetMaxOpenConns(1)
	if _, err := db.Exec(schema); err != nil {
		db.Close()
		return nil, fmt.Errorf("schema: %w", err)
	}
	return &store{db: db}, nil
}

func (s *store) close() error { return s.db.Close() }

func hashToken(tok string) []byte {
	h := sha256.Sum256([]byte(tok))
	return h[:]
}

// --- accounts ---

// invite creates an account for email. Invite-only sign-up means an account
// row is the allowlist: an address with no row can never receive a code.
func (s *store) invite(ctx context.Context, email string) error {
	_, err := s.db.ExecContext(ctx,
		`INSERT INTO users (email, created_at) VALUES (?, ?) ON CONFLICT (email) DO NOTHING`,
		normEmail(email), time.Now().Unix())
	return err
}

// revoke deletes an account and, by cascade, its sessions and games.
func (s *store) revoke(ctx context.Context, email string) (bool, error) {
	res, err := s.db.ExecContext(ctx, `DELETE FROM users WHERE email = ?`, email)
	if err != nil {
		return false, err
	}
	n, err := res.RowsAffected()
	return n > 0, err
}

func (s *store) listUsers(ctx context.Context) ([]string, error) {
	rows, err := s.db.QueryContext(ctx, `SELECT email FROM users ORDER BY email`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []string
	for rows.Next() {
		var e string
		if err := rows.Scan(&e); err != nil {
			return nil, err
		}
		out = append(out, e)
	}
	return out, rows.Err()
}

func (s *store) userByEmail(ctx context.Context, email string) (int64, bool, error) {
	var id int64
	err := s.db.QueryRowContext(ctx, `SELECT id FROM users WHERE email = ?`, email).Scan(&id)
	if errors.Is(err, sql.ErrNoRows) {
		return 0, false, nil
	}
	return id, err == nil, err
}

func (s *store) emailOf(ctx context.Context, userID int64) (string, error) {
	var e string
	err := s.db.QueryRowContext(ctx, `SELECT email FROM users WHERE id = ?`, userID).Scan(&e)
	return e, err
}

// --- sign-in codes ---

// lastCodeSent returns when the user's current code was sent, or the zero
// time if there is none.
func (s *store) lastCodeSent(ctx context.Context, userID int64) (time.Time, error) {
	var at int64
	err := s.db.QueryRowContext(ctx, `SELECT sent_at FROM login_codes WHERE user_id = ?`, userID).Scan(&at)
	if errors.Is(err, sql.ErrNoRows) {
		return time.Time{}, nil
	}
	if err != nil {
		return time.Time{}, err
	}
	return time.Unix(at, 0), nil
}

// putCode replaces the user's code. One live code per account: asking for a
// new one cancels the old, so an old email can't be used after a resend.
func (s *store) putCode(ctx context.Context, userID int64, code string, ttl time.Duration) error {
	now := time.Now()
	_, err := s.db.ExecContext(ctx, `
		INSERT INTO login_codes (user_id, code_hash, expires_at, attempts, sent_at) VALUES (?, ?, ?, 0, ?)
		ON CONFLICT (user_id) DO UPDATE SET code_hash = excluded.code_hash,
			expires_at = excluded.expires_at, attempts = 0, sent_at = excluded.sent_at`,
		userID, hashToken(code), now.Add(ttl).Unix(), now.Unix())
	return err
}

// errCodeInvalid covers a wrong, expired, missing, or exhausted code. The
// caller learns nothing about which.
var errCodeInvalid = errors.New("invalid code")

// useCode checks a code and consumes it on success. Each wrong guess counts
// against the code; after maxAttempts the code is dead even if the next guess
// is right, which caps a six-digit code at a 1-in-200,000 guessing chance.
func (s *store) useCode(ctx context.Context, userID int64, code string, maxAttempts int) error {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()

	var stored []byte
	var expires, attempts int64
	err = tx.QueryRowContext(ctx,
		`SELECT code_hash, expires_at, attempts FROM login_codes WHERE user_id = ?`, userID).
		Scan(&stored, &expires, &attempts)
	if errors.Is(err, sql.ErrNoRows) {
		return errCodeInvalid
	}
	if err != nil {
		return err
	}
	if time.Now().Unix() > expires || attempts >= int64(maxAttempts) {
		return errCodeInvalid
	}
	if subtle.ConstantTimeCompare(stored, hashToken(code)) != 1 {
		if _, err := tx.ExecContext(ctx,
			`UPDATE login_codes SET attempts = attempts + 1 WHERE user_id = ?`, userID); err != nil {
			return err
		}
		if err := tx.Commit(); err != nil {
			return err
		}
		return errCodeInvalid
	}
	if _, err := tx.ExecContext(ctx, `DELETE FROM login_codes WHERE user_id = ?`, userID); err != nil {
		return err
	}
	return tx.Commit()
}

// --- sessions ---

func (s *store) createSession(ctx context.Context, userID int64, token string, ttl time.Duration) error {
	_, err := s.db.ExecContext(ctx,
		`INSERT INTO sessions (token_hash, user_id, expires_at) VALUES (?, ?, ?)`,
		hashToken(token), userID, time.Now().Add(ttl).Unix())
	return err
}

func (s *store) sessionUser(ctx context.Context, token string) (int64, bool, error) {
	var id int64
	err := s.db.QueryRowContext(ctx,
		`SELECT user_id FROM sessions WHERE token_hash = ? AND expires_at > ?`,
		hashToken(token), time.Now().Unix()).Scan(&id)
	if errors.Is(err, sql.ErrNoRows) {
		return 0, false, nil
	}
	return id, err == nil, err
}

func (s *store) deleteSession(ctx context.Context, token string) error {
	_, err := s.db.ExecContext(ctx, `DELETE FROM sessions WHERE token_hash = ?`, hashToken(token))
	return err
}

// sweep removes expired sessions and codes. Expired rows are already
// ignored by every query; this only stops them from piling up.
func (s *store) sweep(ctx context.Context) error {
	now := time.Now().Unix()
	_, err := s.db.ExecContext(ctx, `DELETE FROM sessions WHERE expires_at <= ?`, now)
	if err != nil {
		return err
	}
	_, err = s.db.ExecContext(ctx, `DELETE FROM login_codes WHERE expires_at <= ?`, now)
	return err
}

// --- sync ---

// syncGame is one game as the sync API sends it. Doc is the client's JSON,
// stored and returned byte for byte.
type syncGame struct {
	ID  string `json:"id"`
	Rev int64  `json:"rev"`
	Doc string `json:"doc"`
}

// errConflict means the client's base revision is not the server's current
// one: another device saved first.
var errConflict = errors.New("revision conflict")

// putGame saves a game if baseRev matches the stored revision (0 for a game
// the server hasn't seen), and returns the new revision. On a mismatch it
// returns errConflict and the server's copy, which the client adopts: the
// first device to sync wins, the stale one loses its unsynced moves.
func (s *store) putGame(ctx context.Context, userID int64, g syncGame, baseRev int64) (int64, *syncGame, error) {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return 0, nil, err
	}
	defer tx.Rollback()

	var cur syncGame
	err = tx.QueryRowContext(ctx, `SELECT id, rev, doc FROM games WHERE user_id = ? AND id = ?`,
		userID, g.ID).Scan(&cur.ID, &cur.Rev, &cur.Doc)
	switch {
	case errors.Is(err, sql.ErrNoRows):
		cur.Rev = 0
	case err != nil:
		return 0, nil, err
	}
	if cur.Rev != baseRev {
		if cur.Rev == 0 {
			// The client thinks the server has a revision it doesn't. Only a
			// wiped server or a revoked-and-reinvited account gets here;
			// treat the client's copy as new rather than lose it.
			baseRev = 0
		} else {
			return 0, &cur, errConflict
		}
	}

	seq, err := nextSeq(ctx, tx, userID)
	if err != nil {
		return 0, nil, err
	}
	rev := cur.Rev + 1
	_, err = tx.ExecContext(ctx, `
		INSERT INTO games (user_id, id, rev, seq, doc, updated_at) VALUES (?, ?, ?, ?, ?, ?)
		ON CONFLICT (user_id, id) DO UPDATE SET rev = excluded.rev, seq = excluded.seq,
			doc = excluded.doc, updated_at = excluded.updated_at`,
		userID, g.ID, rev, seq, g.Doc, time.Now().Unix())
	if err != nil {
		return 0, nil, err
	}
	return rev, nil, tx.Commit()
}

func nextSeq(ctx context.Context, tx *sql.Tx, userID int64) (int64, error) {
	var seq int64
	err := tx.QueryRowContext(ctx,
		`UPDATE users SET seq = seq + 1 WHERE id = ? RETURNING seq`, userID).Scan(&seq)
	return seq, err
}

// changes returns every game written after seq, plus the settings if they
// changed after seq, and the cursor to pass next time.
func (s *store) changes(ctx context.Context, userID, since int64) (games []syncGame, settings *syncSettings, cursor int64, err error) {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return nil, nil, 0, err
	}
	defer tx.Rollback()

	var ss syncSettings
	var settingsSeq int64
	if err := tx.QueryRowContext(ctx,
		`SELECT seq, settings, settings_rev, settings_seq FROM users WHERE id = ?`, userID).
		Scan(&cursor, &ss.Doc, &ss.Rev, &settingsSeq); err != nil {
		return nil, nil, 0, err
	}
	if settingsSeq > since {
		settings = &ss
	}

	rows, err := tx.QueryContext(ctx,
		`SELECT id, rev, doc FROM games WHERE user_id = ? AND seq > ? ORDER BY seq`, userID, since)
	if err != nil {
		return nil, nil, 0, err
	}
	defer rows.Close()
	for rows.Next() {
		var g syncGame
		if err := rows.Scan(&g.ID, &g.Rev, &g.Doc); err != nil {
			return nil, nil, 0, err
		}
		games = append(games, g)
	}
	return games, settings, cursor, rows.Err()
}

// syncSettings is the account's settings document: mistake checking and any
// other preference that follows the player between devices.
type syncSettings struct {
	Rev int64  `json:"rev"`
	Doc string `json:"doc"`
}

// putSettings follows the same revision rule as putGame.
func (s *store) putSettings(ctx context.Context, userID int64, doc string, baseRev int64) (int64, *syncSettings, error) {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return 0, nil, err
	}
	defer tx.Rollback()

	var cur syncSettings
	if err := tx.QueryRowContext(ctx, `SELECT settings, settings_rev FROM users WHERE id = ?`, userID).
		Scan(&cur.Doc, &cur.Rev); err != nil {
		return 0, nil, err
	}
	if cur.Rev != baseRev {
		return 0, &cur, errConflict
	}
	seq, err := nextSeq(ctx, tx, userID)
	if err != nil {
		return 0, nil, err
	}
	if _, err := tx.ExecContext(ctx,
		`UPDATE users SET settings = ?, settings_rev = settings_rev + 1, settings_seq = ? WHERE id = ?`,
		doc, seq, userID); err != nil {
		return 0, nil, err
	}
	return cur.Rev + 1, nil, tx.Commit()
}

// normEmail lower-cases and trims an address. The column is COLLATE NOCASE
// as well; normalising here keeps logs and rate-limit keys consistent.
func normEmail(e string) string { return strings.ToLower(strings.TrimSpace(e)) }
