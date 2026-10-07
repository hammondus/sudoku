package sudoku

import (
	"context"
	"math/rand/v2"
	"testing"
	"time"
)

// Project Euler problem 96, grid 01: a well-known puzzle that falls to
// singles alone.
const eulerEasy = "003020600900305001001806400008102900700000008006708200002609500800203009005010300"

// Arto Inkala's 2012 "world's hardest sudoku". It has one solution but needs
// far more than the techniques graded here.
const inkala = "800000000003600000070090200050007000000045700000100030001000068008500010090000400"

func mustParse(t *testing.T, s string) Grid {
	t.Helper()
	g, err := Parse(s)
	if err != nil {
		t.Fatal(err)
	}
	return g
}

func TestParseRoundTrip(t *testing.T) {
	g := mustParse(t, eulerEasy)
	if got := mustParse(t, g.String()); got != g {
		t.Fatalf("round trip changed the grid")
	}
	for _, bad := range []string{"", "12", eulerEasy + "1", eulerEasy[:80] + "x"} {
		if _, err := Parse(bad); err == nil {
			t.Errorf("Parse(%q) succeeded, want error", bad)
		}
	}
}

func TestUnits(t *testing.T) {
	for c := range 81 {
		n := 0
		for p := range 81 {
			if isPeer[c][p] {
				n++
			}
		}
		if n != 20 {
			t.Fatalf("cell %d has %d peers, want 20", c, n)
		}
	}
	if boxOf(80) != 8 || boxOf(30) != 4 || units[22][4] != 40 {
		t.Fatal("box geometry is wrong")
	}
}

func TestCountSolutions(t *testing.T) {
	if n := CountSolutions(mustParse(t, eulerEasy), 2); n != 1 {
		t.Errorf("euler: %d solutions, want 1", n)
	}
	if n := CountSolutions(mustParse(t, inkala), 2); n != 1 {
		t.Errorf("inkala: %d solutions, want 1", n)
	}
	if n := CountSolutions(Grid{}, 2); n != 2 {
		t.Errorf("empty grid: %d solutions counted, want limit 2", n)
	}
	g := mustParse(t, eulerEasy)
	g[0] = 2 // the 2 already in row 1
	if n := CountSolutions(g, 2); n != 0 {
		t.Errorf("conflicting givens: %d solutions, want 0", n)
	}
}

func TestGradeKnown(t *testing.T) {
	if l := Grade(mustParse(t, eulerEasy)).Level; l != Easy {
		t.Errorf("euler graded %v, want easy", l)
	}
	if l := Grade(mustParse(t, inkala)).Level; l != TooHard {
		t.Errorf("inkala graded %v, want too hard", l)
	}
}

// checkSound replays a grading and fails if any step places a wrong digit or
// removes the right one. This is the test every technique has to pass: a
// finder that is wrong even once produces hints that lie to the player.
func checkSound(t *testing.T, g Grid, seen map[Technique]int) {
	t.Helper()
	sol, ok := Solve(g)
	if !ok {
		t.Fatal("puzzle has no solution")
	}
	b := newBoard(g)
	for !b.solved() {
		s, ok := b.next()
		if !ok {
			return
		}
		seen[s.Technique]++
		if s.Place != nil && sol[s.Place.Cell] != s.Place.Digit {
			t.Fatalf("%v placed %d in %s, solution is %d\npuzzle %s\n%s",
				s.Technique, s.Place.Digit, CellName(s.Place.Cell), sol[s.Place.Cell], g, s.Text)
		}
		for _, e := range s.Eliminations {
			if sol[e.Cell] == e.Digit {
				t.Fatalf("%v removed the solution digit %d from %s\npuzzle %s\n%s",
					s.Technique, e.Digit, CellName(e.Cell), g, s.Text)
			}
			if b.cand[e.Cell]&(1<<e.Digit) == 0 {
				t.Fatalf("%v removed %d from %s, which wasn't a candidate", s.Technique, e.Digit, CellName(e.Cell))
			}
		}
		if s.Text == "" {
			t.Fatalf("%v step has no text", s.Technique)
		}
		b.apply(s)
	}
	if b.g != sol {
		t.Fatal("grader finished on a grid that isn't the solution")
	}
}

func TestGenerateEachLevel(t *testing.T) {
	rng := rand.New(rand.NewPCG(1, 2))
	seen := map[Technique]int{}
	per := 15
	if testing.Short() {
		per = 3
	}
	for l := Easy; l <= Expert; l++ {
		for range per {
			ctx, cancel := context.WithTimeout(t.Context(), 30*time.Second)
			p, err := Generate(ctx, l, rng)
			cancel()
			if err != nil {
				t.Fatalf("%v: %v", l, err)
			}
			if CountSolutions(p.Givens, 2) != 1 {
				t.Fatalf("%v puzzle %s is not unique", l, p.Givens)
			}
			if got := Grade(p.Givens).Level; got != l {
				t.Fatalf("asked for %v, got a puzzle grading %v", l, got)
			}
			if p.Givens.Clues() < clueFloor[l] {
				t.Fatalf("%v puzzle has %d clues, under the floor", l, p.Givens.Clues())
			}
			for c, v := range p.Givens {
				if v != 0 && v != p.Solution[c] {
					t.Fatalf("given disagrees with solution at %s", CellName(c))
				}
			}
			checkSound(t, p.Givens, seen)
		}
	}
	for tq := HiddenSingle; tq <= SimpleColouring; tq++ {
		t.Logf("%-18v used %d times", tq, seen[tq])
	}
}

// TestSoundRandomDigs grades puzzles dug with no grade limit, which reach far
// more advanced positions than generated puzzles do, so every finder sees
// plenty of boards where it applies.
func TestSoundRandomDigs(t *testing.T) {
	rng := rand.New(rand.NewPCG(3, 4))
	seen := map[Technique]int{}
	n := 1000
	if testing.Short() {
		n = 40
	}
	for range n {
		g := randomSolution(rng)
		for _, c := range rng.Perm(81) {
			d := g[c]
			g[c] = 0
			if CountSolutions(g, 2) != 1 {
				g[c] = d
			}
		}
		checkSound(t, g, seen)
	}
	for tq := HiddenSingle; tq <= SimpleColouring; tq++ {
		t.Logf("%-18v used %d times", tq, seen[tq])
	}
	for tq := HiddenSingle; tq <= SimpleColouring; tq++ {
		if seen[tq] == 0 {
			t.Errorf("%v never applied; its soundness is untested", tq)
		}
	}
}

func TestNextHint(t *testing.T) {
	givens := mustParse(t, eulerEasy)
	sol, _ := Solve(givens)

	h, err := NextHint(givens, givens)
	if err != nil {
		t.Fatal(err)
	}
	last := h.Steps[len(h.Steps)-1]
	if last.Place == nil || sol[last.Place.Cell] != last.Place.Digit {
		t.Fatalf("hint does not end in a correct placement: %+v", last)
	}

	board := givens
	wrong := 0
	for board[wrong] != 0 {
		wrong++
	}
	board[wrong] = sol[wrong]%9 + 1
	h, err = NextHint(givens, board)
	if err != nil {
		t.Fatal(err)
	}
	if len(h.Mistakes) != 1 || h.Mistakes[0] != wrong || len(h.Steps) != 0 {
		t.Fatalf("want one mistake at %d and no steps, got %+v", wrong, h)
	}

	if h, _ := NextHint(givens, sol); len(h.Steps) != 0 {
		t.Fatal("solved board still produced a hint")
	}
	if _, err := NextHint(Grid{}, Grid{}); err == nil {
		t.Fatal("empty givens accepted")
	}
	board = givens
	board[2] = 4 // R1C3 is a given 3
	if _, err := NextHint(givens, board); err == nil {
		t.Fatal("changed given accepted")
	}
}

func TestCombos(t *testing.T) {
	n := 0
	for range combos(6, 3) {
		n++
	}
	if n != 20 {
		t.Fatalf("C(6,3) yielded %d, want 20", n)
	}
	for range combos(2, 3) {
		t.Fatal("k > n yielded a combination")
	}
}

func BenchmarkGenerate(b *testing.B) {
	for l := Easy; l <= Expert; l++ {
		b.Run(l.String(), func(b *testing.B) {
			rng := rand.New(rand.NewPCG(5, 6))
			for b.Loop() {
				if _, err := Generate(b.Context(), l, rng); err != nil {
					b.Fatal(err)
				}
			}
		})
	}
}
