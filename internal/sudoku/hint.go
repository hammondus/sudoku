package sudoku

import "errors"

// Hint is the answer to "what should I do next?" for a board in play.
type Hint struct {
	// Mistakes lists cells whose digit disagrees with the solution. When it
	// is not empty, Steps is empty: a deduction from a wrong board would be
	// wrong too.
	Mistakes []int `json:"mistakes,omitempty"`
	// Steps ends with the easiest placement available, preceded by any
	// eliminations needed to reach it. Empty means the board is solved or
	// needs a technique this package doesn't know.
	Steps []Step `json:"steps,omitempty"`
}

// ErrNoSolution means the givens don't form a puzzle with one solution.
var ErrNoSolution = errors.New("sudoku: givens do not have a unique solution")

// NextHint explains the next move on board, which holds the givens plus the
// player's digits. Pencil marks are not an input: the player's marks may be
// incomplete or wrong, so the hint works from candidates it computes itself.
func NextHint(givens, board Grid) (Hint, error) {
	if CountSolutions(givens, 2) != 1 {
		return Hint{}, ErrNoSolution
	}
	solution, _ := Solve(givens)

	var h Hint
	for c, v := range board {
		if givens[c] != 0 && v != givens[c] {
			return Hint{}, errors.New("sudoku: board changes a given")
		}
		if v != 0 && v != solution[c] {
			h.Mistakes = append(h.Mistakes, c)
		}
	}
	if len(h.Mistakes) > 0 {
		return h, nil
	}

	b := newBoard(board)
	for !b.solved() {
		s, ok := b.next()
		if !ok {
			return Hint{}, nil
		}
		h.Steps = append(h.Steps, s)
		if s.Place != nil {
			break
		}
		b.apply(s)
	}
	return h, nil
}
