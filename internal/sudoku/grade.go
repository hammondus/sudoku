package sudoku

import (
	"fmt"
	"math/bits"
)

// Level is a difficulty grade. A puzzle's level is the level of the hardest
// technique the grader needed to solve it.
type Level uint8

const (
	// TooHard means the grader's techniques stall before the puzzle is
	// solved. Such puzzles still have a unique solution; they need
	// techniques (long chains, forcing nets) this package doesn't grade, so
	// the generator throws them away.
	TooHard Level = iota
	Easy
	Medium
	Hard
	Expert
)

var levelNames = [...]string{"too hard", "easy", "medium", "hard", "expert"}

func (l Level) String() string {
	if int(l) < len(levelNames) {
		return levelNames[l]
	}
	return fmt.Sprintf("Level(%d)", l)
}

// ParseLevel is the inverse of Level.String for the four playable levels.
func ParseLevel(s string) (Level, error) {
	for l := Easy; l <= Expert; l++ {
		if levelNames[l] == s {
			return l, nil
		}
	}
	return 0, fmt.Errorf("sudoku: unknown level %q", s)
}

// Technique is one human solving technique.
type Technique uint8

// Techniques in the order the grader tries them: easiest first, so every step
// it records is the easiest one available at that point.
const (
	HiddenSingle Technique = iota + 1
	NakedSingle
	Pointing
	BoxLine
	NakedPair
	HiddenPair
	NakedTriple
	HiddenTriple
	XWing
	XYWing
	NakedQuad
	Swordfish
	XYZWing
	SimpleColouring
)

var techniqueInfo = [...]struct {
	name  string
	level Level
}{
	HiddenSingle:    {"Hidden single", Easy},
	NakedSingle:     {"Naked single", Easy},
	Pointing:        {"Pointing pair", Medium},
	BoxLine:         {"Box/line reduction", Medium},
	NakedPair:       {"Naked pair", Medium},
	HiddenPair:      {"Hidden pair", Medium},
	NakedTriple:     {"Naked triple", Medium},
	HiddenTriple:    {"Hidden triple", Medium},
	XWing:           {"X-Wing", Hard},
	XYWing:          {"XY-Wing", Hard},
	NakedQuad:       {"Naked quad", Hard},
	Swordfish:       {"Swordfish", Hard},
	XYZWing:         {"XYZ-Wing", Expert},
	SimpleColouring: {"Simple colouring", Expert},
}

func (t Technique) String() string { return techniqueInfo[t].name }

// MarshalText makes a Technique appear by name in JSON.
func (t Technique) MarshalText() ([]byte, error) { return []byte(t.String()), nil }

// Level returns the difficulty a puzzle earns by needing this technique.
func (t Technique) Level() Level { return techniqueInfo[t].level }

// Candidate is a digit that may go in a cell.
type Candidate struct {
	Cell  int   `json:"cell"`
	Digit uint8 `json:"digit"`
}

// Step is one deduction: either a digit placed (singles) or candidates
// removed (everything else). Cells and Alt name the cells that make up the
// pattern, for highlighting; Alt is the second group where a technique has
// two (pincers of a wing, the second colour of a chain). Units lists the
// houses the deduction is about. Text explains the step in words.
type Step struct {
	Technique    Technique   `json:"technique"`
	Place        *Candidate  `json:"place,omitempty"`
	Eliminations []Candidate `json:"eliminations,omitempty"`
	Cells        []int       `json:"cells,omitempty"`
	Alt          []int       `json:"alt,omitempty"`
	Units        []int       `json:"units,omitempty"`
	Text         string      `json:"text"`
}

// board is the grader's working state: placed digits plus pencil marks.
// cand[c] uses the same bit layout as the solver (bit d = digit d) and is zero
// for a filled cell.
type board struct {
	g    Grid
	cand [81]uint16
}

func newBoard(g Grid) *board {
	b := &board{g: g}
	for c, v := range g {
		if v != 0 {
			continue
		}
		m := allDigits
		for _, p := range peers[c] {
			m &^= 1 << g[p]
		}
		b.cand[c] = m
	}
	return b
}

func (b *board) place(c int, d uint8) {
	b.g[c] = d
	b.cand[c] = 0
	for _, p := range peers[c] {
		b.cand[p] &^= 1 << d
	}
}

func (b *board) apply(s Step) {
	if s.Place != nil {
		b.place(s.Place.Cell, s.Place.Digit)
	}
	for _, e := range s.Eliminations {
		b.cand[e.Cell] &^= 1 << e.Digit
	}
}

func (b *board) solved() bool {
	for _, v := range b.g {
		if v == 0 {
			return false
		}
	}
	return true
}

// next returns the easiest step available, and false if no technique applies.
func (b *board) next() (Step, bool) {
	for _, f := range finders {
		if s, ok := f(b); ok {
			return s, true
		}
	}
	return Step{}, false
}

// Result is the grader's verdict on a puzzle.
type Result struct {
	Level Level
	Steps []Step
}

// Grade solves g with human techniques only and reports the hardest one it
// needed. g must have a unique solution; Grade does not check.
func Grade(g Grid) Result {
	b := newBoard(g)
	var r Result
	hardest := Easy
	for !b.solved() {
		s, ok := b.next()
		if !ok {
			r.Level = TooHard
			return r
		}
		hardest = max(hardest, s.Technique.Level())
		r.Steps = append(r.Steps, s)
		b.apply(s)
	}
	r.Level = hardest
	return r
}

// gradeLevel is Grade without the step log, for the generator's inner loop.
func gradeLevel(g Grid) Level {
	b := newBoard(g)
	hardest := Easy
	for !b.solved() {
		s, ok := b.next()
		if !ok {
			return TooHard
		}
		hardest = max(hardest, s.Technique.Level())
		b.apply(s)
	}
	return hardest
}

func popcount(m uint16) int { return bits.OnesCount16(m) }

// digitsOf lists the digits set in a candidate mask, ascending.
func digitsOf(m uint16) []uint8 {
	ds := make([]uint8, 0, popcount(m))
	for ; m != 0; m &= m - 1 {
		ds = append(ds, uint8(bits.TrailingZeros16(m)))
	}
	return ds
}
