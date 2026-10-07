package main

import (
	"context"
	"encoding/json"
	"errors"
	"log/slog"
	"math/rand/v2"
	"net/http"
	"strconv"
	"time"
	"uuid"

	"github.com/hammondus/nitrokit"
	"github.com/hammondus/sudoku/internal/sudoku"
)

// api serves puzzles and hints to anyone, and sync to signed-in players.
// Puzzles and hints are open because guests play too; they cost CPU, so a
// per-address limiter and a global concurrency cap bound them.
type api struct {
	st    *store
	log   *slog.Logger
	trust *nitrokit.ProxyTrust
	limit *nitrokit.Limiter
	// work caps puzzle generation and hint solving running at once. Each
	// takes up to tens of milliseconds of one core; without a cap a burst
	// from many addresses would queue unbounded goroutines.
	work chan struct{}
}

func newAPI(st *store, log *slog.Logger, trust *nitrokit.ProxyTrust, workers int) *api {
	return &api{
		st: st, log: log, trust: trust,
		// A client fills a buffer of 5 puzzles per level on first load: 20
		// requests at once. After that it asks for one per finished game.
		limit: nitrokit.NewLimiter(0.5, 40),
		work:  make(chan struct{}, workers),
	}
}

// acquire takes a work slot, giving up when ctx ends.
func (a *api) acquire(ctx context.Context) bool {
	select {
	case a.work <- struct{}{}:
		return true
	case <-ctx.Done():
		return false
	}
}

func (a *api) release() { <-a.work }

func (a *api) allow(w http.ResponseWriter, r *http.Request) bool {
	ok, retry := a.limit.Allow(a.trust.ClientIP(r).String())
	if !ok {
		tooMany(w, retry)
	}
	return ok
}

// puzzleResponse carries everything the client needs to play offline: the
// solution, for mistake checking, and the grader's solving path, for hints
// when the hint endpoint can't be reached.
type puzzleResponse struct {
	Level    string        `json:"level"`
	Givens   string        `json:"givens"`
	Solution string        `json:"solution"`
	Steps    []sudoku.Step `json:"steps"`
}

func (a *api) handlePuzzle(w http.ResponseWriter, r *http.Request) {
	level, err := sudoku.ParseLevel(r.URL.Query().Get("level"))
	if err != nil {
		nitrokit.JSONError(w, http.StatusBadRequest, "level must be easy, medium, hard, or expert")
		return
	}
	if !a.allow(w, r) {
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), 10*time.Second)
	defer cancel()
	if !a.acquire(ctx) {
		nitrokit.JSONError(w, http.StatusServiceUnavailable, "The server is busy. Try again.")
		return
	}
	defer a.release()

	rng := rand.New(rand.NewPCG(rand.Uint64(), rand.Uint64()))
	p, err := sudoku.Generate(ctx, level, rng)
	if err != nil {
		a.log.Warn("generate", "level", level, "err", err)
		nitrokit.JSONError(w, http.StatusServiceUnavailable, "Couldn't make a puzzle in time. Try again.")
		return
	}
	nitrokit.NoStore(w) // every response is a different puzzle
	nitrokit.WriteJSON(w, http.StatusOK, puzzleResponse{
		Level:    level.String(),
		Givens:   p.Givens.String(),
		Solution: p.Solution.String(),
		Steps:    sudoku.Grade(p.Givens).Steps,
	})
}

// handleHint is stateless: the client sends the givens and its board, so the
// server keeps no record of games in progress and a guest needs no account.
func (a *api) handleHint(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Givens string `json:"givens"`
		Board  string `json:"board"`
	}
	if !nitrokit.ReadJSON(w, r, &req, 4<<10) {
		return
	}
	givens, err1 := sudoku.Parse(req.Givens)
	board, err2 := sudoku.Parse(req.Board)
	if err := errors.Join(err1, err2); err != nil {
		nitrokit.JSONError(w, http.StatusBadRequest, err.Error())
		return
	}
	if !a.allow(w, r) {
		return
	}
	if !a.acquire(r.Context()) {
		return
	}
	defer a.release()

	h, err := sudoku.NextHint(givens, board)
	if err != nil {
		nitrokit.JSONError(w, http.StatusBadRequest, err.Error())
		return
	}
	nitrokit.NoStore(w)
	nitrokit.WriteJSON(w, http.StatusOK, h)
}

// maxDoc bounds one game or settings document. A game is about 2 KB of JSON
// (two 81-character grids, entries, pencil marks, a move log for undo); 64 KB
// leaves room without letting one account fill the disk.
const maxDoc = 64 << 10

func (a *api) handleSync(w http.ResponseWriter, r *http.Request, userID int64) {
	var since int64
	if s := r.URL.Query().Get("since"); s != "" {
		var err error
		if since, err = strconv.ParseInt(s, 10, 64); err != nil || since < 0 {
			nitrokit.JSONError(w, http.StatusBadRequest, "since must be a non-negative integer")
			return
		}
	}
	games, settings, cursor, err := a.st.changes(r.Context(), userID, since)
	if err != nil {
		internalError(w, a.log, "sync: changes", err)
		return
	}
	nitrokit.NoCachePrivate(w)
	nitrokit.WriteJSON(w, http.StatusOK, struct {
		Cursor   int64         `json:"cursor"`
		Games    []syncGame    `json:"games"`
		Settings *syncSettings `json:"settings,omitempty"`
	}{cursor, nonNil(games), settings})
}

func nonNil(g []syncGame) []syncGame {
	if g == nil {
		return []syncGame{}
	}
	return g
}

type putRequest struct {
	BaseRev int64           `json:"base_rev"`
	Doc     json.RawMessage `json:"doc"`
}

func (a *api) readPut(w http.ResponseWriter, r *http.Request) (putRequest, bool) {
	var req putRequest
	if !nitrokit.ReadJSON(w, r, &req, maxDoc+1024) {
		return req, false
	}
	if len(req.Doc) == 0 || len(req.Doc) > maxDoc || !json.Valid(req.Doc) {
		nitrokit.JSONError(w, http.StatusBadRequest, "doc must be a JSON value of at most 64 KB")
		return req, false
	}
	return req, true
}

func (a *api) handlePutGame(w http.ResponseWriter, r *http.Request, userID int64) {
	id := r.PathValue("id")
	if _, err := uuid.Parse(id); err != nil {
		nitrokit.JSONError(w, http.StatusBadRequest, "game id must be a UUID")
		return
	}
	req, ok := a.readPut(w, r)
	if !ok {
		return
	}
	rev, cur, err := a.st.putGame(r.Context(), userID, syncGame{ID: id, Doc: string(req.Doc)}, req.BaseRev)
	nitrokit.NoCachePrivate(w)
	switch {
	case errors.Is(err, errConflict):
		nitrokit.WriteJSON(w, http.StatusConflict, map[string]any{"game": cur})
	case err != nil:
		internalError(w, a.log, "sync: put game", err)
	default:
		nitrokit.WriteJSON(w, http.StatusOK, map[string]int64{"rev": rev})
	}
}

func (a *api) handlePutSettings(w http.ResponseWriter, r *http.Request, userID int64) {
	req, ok := a.readPut(w, r)
	if !ok {
		return
	}
	rev, cur, err := a.st.putSettings(r.Context(), userID, string(req.Doc), req.BaseRev)
	nitrokit.NoCachePrivate(w)
	switch {
	case errors.Is(err, errConflict):
		nitrokit.WriteJSON(w, http.StatusConflict, map[string]any{"settings": cur})
	case err != nil:
		internalError(w, a.log, "sync: put settings", err)
	default:
		nitrokit.WriteJSON(w, http.StatusOK, map[string]int64{"rev": rev})
	}
}
