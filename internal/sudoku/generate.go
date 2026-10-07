package sudoku

import (
	"context"
	"math/rand/v2"
)

// Puzzle is a generated puzzle with its solution and grade.
type Puzzle struct {
	Givens   Grid
	Solution Grid
	Level    Level
}

// clueFloor stops the generator from removing clues below a count, per level.
// Difficulty comes from technique, not clue count, but players also read a
// sparse grid as hard: an "easy" puzzle with 24 clues solvable by singles
// alone is still a long slog. Floors keep Easy and Medium looking their part.
// Hard and Expert have none: they are dug as far as uniqueness allows.
var clueFloor = [...]int{Easy: 36, Medium: 30, Hard: 0, Expert: 0}

// Generate makes a puzzle of the given level. It retries until a puzzle
// grades at exactly that level, so its run time varies; ctx bounds it.
//
// Each attempt fills a random complete grid, then visits the cells in random
// order and empties each one if the puzzle stays uniquely solvable and its
// grade stays at or below the target. The grade check during digging, not
// only at the end, is what makes Hard and Expert reachable: without it the
// digger runs straight past them into puzzles this package can't grade.
func Generate(ctx context.Context, level Level, rng *rand.Rand) (Puzzle, error) {
	for {
		if err := ctx.Err(); err != nil {
			return Puzzle{}, err
		}
		if p, ok := attempt(level, rng); ok {
			return p, nil
		}
	}
}

func attempt(level Level, rng *rand.Rand) (Puzzle, bool) {
	solution := randomSolution(rng)
	g := solution
	clues := 81
	for _, c := range rng.Perm(81) {
		if clues <= clueFloor[level] {
			break
		}
		d := g[c]
		g[c] = 0
		if CountSolutions(g, 2) != 1 {
			g[c] = d
			continue
		}
		if l := gradeLevel(g); l == TooHard || l > level {
			g[c] = d
			continue
		}
		clues--
	}
	if gradeLevel(g) != level {
		return Puzzle{}, false
	}
	return Puzzle{Givens: g, Solution: solution, Level: level}, true
}
