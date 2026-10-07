package sudoku

import (
	"math/bits"
	"math/rand/v2"
)

// The brute-force solver: depth-first search that always branches on the
// empty cell with the fewest candidates. It answers two questions the
// generator needs — "what is a complete grid?" and "is this solution unique?"
// — and nothing about difficulty. Human-style solving lives in grade.go.

// search holds the digits used in each row, column, and box as bitmasks
// (bit d set means digit d is used), so a cell's candidates are one OR and
// one NOT away.
type search struct {
	g        Grid
	row, col [9]uint16
	box      [9]uint16
	rng      *rand.Rand // nil: try candidates in order
	limit    int
	count    int
	first    Grid
}

const allDigits uint16 = 0b11_1111_1110 // bits 1–9

func newSearch(g Grid) (*search, bool) {
	s := &search{g: g}
	for c, v := range g {
		if v == 0 {
			continue
		}
		bit := uint16(1) << v
		r, k, b := rowOf(c), colOf(c), boxOf(c)
		if (s.row[r]|s.col[k]|s.box[b])&bit != 0 {
			return nil, false // a given repeats in a house
		}
		s.row[r] |= bit
		s.col[k] |= bit
		s.box[b] |= bit
	}
	return s, true
}

func (s *search) cands(c int) uint16 {
	return allDigits &^ (s.row[rowOf(c)] | s.col[colOf(c)] | s.box[boxOf(c)])
}

// run returns true once count reaches limit, to unwind the recursion.
func (s *search) run() bool {
	best, bestN := -1, 10
	var bestMask uint16
	for c, v := range s.g {
		if v != 0 {
			continue
		}
		m := s.cands(c)
		n := bits.OnesCount16(m)
		if n == 0 {
			return false
		}
		if n < bestN {
			best, bestN, bestMask = c, n, m
			if n == 1 {
				break
			}
		}
	}
	if best < 0 {
		if s.count == 0 {
			s.first = s.g
		}
		s.count++
		return s.count >= s.limit
	}

	digits := make([]uint8, 0, 9)
	for m := bestMask; m != 0; m &= m - 1 {
		digits = append(digits, uint8(bits.TrailingZeros16(m)))
	}
	if s.rng != nil {
		s.rng.Shuffle(len(digits), func(i, j int) { digits[i], digits[j] = digits[j], digits[i] })
	}

	r, k, b := rowOf(best), colOf(best), boxOf(best)
	for _, d := range digits {
		bit := uint16(1) << d
		s.g[best] = d
		s.row[r] |= bit
		s.col[k] |= bit
		s.box[b] |= bit
		done := s.run()
		s.row[r] &^= bit
		s.col[k] &^= bit
		s.box[b] &^= bit
		s.g[best] = 0
		if done {
			return true
		}
	}
	return false
}

// CountSolutions returns the number of solutions of g, counting no further
// than limit. CountSolutions(g, 2) == 1 is the uniqueness test.
func CountSolutions(g Grid, limit int) int {
	s, ok := newSearch(g)
	if !ok {
		return 0
	}
	s.limit = limit
	s.run()
	return s.count
}

// Solve returns the first solution of g, and false if it has none.
func Solve(g Grid) (Grid, bool) {
	s, ok := newSearch(g)
	if !ok {
		return Grid{}, false
	}
	s.limit = 1
	s.run()
	return s.first, s.count > 0
}

// randomSolution fills an empty grid with digits tried in random order, which
// yields a uniformly shuffled complete grid.
func randomSolution(rng *rand.Rand) Grid {
	s, _ := newSearch(Grid{})
	s.rng = rng
	s.limit = 1
	s.run()
	return s.first
}
