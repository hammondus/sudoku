// Package sudoku generates, solves, and grades 9×9 sudoku puzzles.
//
// Difficulty is graded the way a person experiences it: by the hardest
// solving technique the puzzle forces, not by how many clues it has. The
// grader in grade.go solves with human techniques only, never by guessing,
// and the generator keeps a puzzle only when its grade matches the level asked
// for.
package sudoku

import (
	"errors"
	"fmt"
	"strings"
)

// Grid holds a board in row-major order: index = row*9 + col. A zero is an
// empty cell; 1–9 are digits.
type Grid [81]uint8

// Parse reads 81 characters: 1–9 for a digit, and '0' or '.' for an empty
// cell. Whitespace is ignored so puzzles can be written as nine lines.
func Parse(s string) (Grid, error) {
	var g Grid
	i := 0
	for _, r := range s {
		switch {
		case r == ' ' || r == '\n' || r == '\t' || r == '\r':
			continue
		case i == 81:
			return g, errors.New("sudoku: more than 81 cells")
		case r == '.' || r == '0':
			g[i] = 0
		case r >= '1' && r <= '9':
			g[i] = uint8(r - '0')
		default:
			return g, fmt.Errorf("sudoku: invalid character %q", r)
		}
		i++
	}
	if i != 81 {
		return g, fmt.Errorf("sudoku: %d cells, want 81", i)
	}
	return g, nil
}

// String renders the grid in the 81-character form Parse reads, with '.' for
// an empty cell.
func (g Grid) String() string {
	var b strings.Builder
	b.Grow(81)
	for _, v := range g {
		if v == 0 {
			b.WriteByte('.')
		} else {
			b.WriteByte('0' + v)
		}
	}
	return b.String()
}

// Clues returns the number of filled cells.
func (g Grid) Clues() int {
	n := 0
	for _, v := range g {
		if v != 0 {
			n++
		}
	}
	return n
}

// Valid reports whether no digit repeats in any row, column, or box. It does
// not check that the grid can be completed.
func (g Grid) Valid() bool {
	for _, u := range units {
		var seen uint16
		for _, c := range u {
			if g[c] == 0 {
				continue
			}
			bit := uint16(1) << g[c]
			if seen&bit != 0 {
				return false
			}
			seen |= bit
		}
	}
	return true
}

// A house is a row, column, or box: the 27 groups of nine cells that must each
// hold every digit once. units[0:9] are rows, [9:18] columns, [18:27] boxes.
var (
	units     [27][9]int
	cellUnits [81][3]int // row, column, and box unit index of each cell
	peers     [81][20]int
	isPeer    [81][81]bool
)

func init() {
	for i := range 9 {
		for j := range 9 {
			units[i][j] = i*9 + j   // row i
			units[9+i][j] = j*9 + i // column i
			br, bc := i/3*3, i%3*3  // box i, top-left corner
			units[18+i][j] = (br+j/3)*9 + bc + j%3
		}
	}
	for u, cells := range units {
		for _, c := range cells {
			cellUnits[c][u/9] = u
		}
	}
	for c := range 81 {
		n := 0
		for _, u := range cellUnits[c] {
			for _, p := range units[u] {
				if p != c && !isPeer[c][p] {
					isPeer[c][p] = true
					peers[c][n] = p
					n++
				}
			}
		}
	}
}

func rowOf(c int) int { return c / 9 }
func colOf(c int) int { return c % 9 }
func boxOf(c int) int { return c/27*3 + c%9/3 }

// CellName returns the conventional "R3C5" name of a cell, 1-based.
func CellName(c int) string { return fmt.Sprintf("R%dC%d", rowOf(c)+1, colOf(c)+1) }

// unitName describes a house in words, for hint text.
func unitName(u int) string {
	switch u / 9 {
	case 0:
		return fmt.Sprintf("row %d", u+1)
	case 1:
		return fmt.Sprintf("column %d", u-9+1)
	default:
		return fmt.Sprintf("box %d", u-18+1)
	}
}
