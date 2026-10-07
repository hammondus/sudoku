package sudoku

import (
	"fmt"
	"iter"
	"slices"
	"strings"
)

// Each finder looks for one technique and returns the first instance that
// changes the board. A pattern that removes nothing is not a step: a person
// spotting it learns nothing, so it must not count towards the grade.

type finder func(*board) (Step, bool)

// finders is in Technique order, so board.next returns the easiest step.
var finders = []finder{
	findHiddenSingle,
	findNakedSingle,
	findPointing,
	findBoxLine,
	func(b *board) (Step, bool) { return findNakedSubset(b, 2, NakedPair) },
	func(b *board) (Step, bool) { return findHiddenSubset(b, 2, HiddenPair) },
	func(b *board) (Step, bool) { return findNakedSubset(b, 3, NakedTriple) },
	func(b *board) (Step, bool) { return findHiddenSubset(b, 3, HiddenTriple) },
	func(b *board) (Step, bool) { return findFish(b, 2, XWing) },
	findXYWing,
	func(b *board) (Step, bool) { return findNakedSubset(b, 4, NakedQuad) },
	func(b *board) (Step, bool) { return findFish(b, 3, Swordfish) },
	findXYZWing,
	findSimpleColouring,
}

// unitScanOrder visits boxes before rows and columns: people look for a
// missing digit box by box first, so a hint should too.
var unitScanOrder = func() []int {
	o := make([]int, 0, 27)
	for u := 18; u < 27; u++ {
		o = append(o, u)
	}
	for u := range 18 {
		o = append(o, u)
	}
	return o
}()

// positions returns the empty cells in unit u that still allow digit d.
func (b *board) positions(u int, d uint8) []int {
	var ps []int
	for _, c := range units[u] {
		if b.g[c] == 0 && b.cand[c]&(1<<d) != 0 {
			ps = append(ps, c)
		}
	}
	return ps
}

func (b *board) placedIn(u int, d uint8) bool {
	for _, c := range units[u] {
		if b.g[c] == d {
			return true
		}
	}
	return false
}

func findHiddenSingle(b *board) (Step, bool) {
	for _, u := range unitScanOrder {
		for d := uint8(1); d <= 9; d++ {
			ps := b.positions(u, d)
			if len(ps) != 1 || b.placedIn(u, d) {
				continue
			}
			c := ps[0]
			return Step{
				Technique: HiddenSingle,
				Place:     &Candidate{c, d},
				Cells:     []int{c},
				Units:     []int{u},
				Text:      fmt.Sprintf("In %s, %d can go only in %s.", unitName(u), d, CellName(c)),
			}, true
		}
	}
	return Step{}, false
}

func findNakedSingle(b *board) (Step, bool) {
	for c, v := range b.g {
		if v != 0 || popcount(b.cand[c]) != 1 {
			continue
		}
		d := digitsOf(b.cand[c])[0]
		return Step{
			Technique: NakedSingle,
			Place:     &Candidate{c, d},
			Cells:     []int{c},
			Text: fmt.Sprintf("%s can only be %d: every other digit is already ruled out by its row, column, box, or an earlier deduction.",
				CellName(c), d),
		}, true
	}
	return Step{}, false
}

// findPointing: when a digit's places in a box all lie in one row (or
// column), the digit must go in that row inside the box, so it can't go in the
// rest of the row.
func findPointing(b *board) (Step, bool) {
	for box := 18; box < 27; box++ {
		for d := uint8(1); d <= 9; d++ {
			ps := b.positions(box, d)
			if len(ps) < 2 {
				continue
			}
			for _, line := range []int{cellUnits[ps[0]][0], cellUnits[ps[0]][1]} {
				if !allIn(ps, line) {
					continue
				}
				var elims []Candidate
				for _, c := range b.positions(line, d) {
					if !slices.Contains(ps, c) {
						elims = append(elims, Candidate{c, d})
					}
				}
				if len(elims) == 0 {
					continue
				}
				return Step{
					Technique:    Pointing,
					Eliminations: elims,
					Cells:        ps,
					Units:        []int{box, line},
					Text: fmt.Sprintf("In %s, %d can go only in %s (%s). So %d must be in that part of %s: %s.",
						unitName(box), d, unitName(line), cellList(ps), d, unitName(line), elimText(elims)),
				}, true
			}
		}
	}
	return Step{}, false
}

// findBoxLine is pointing the other way round: a digit confined to one box
// within a row or column can't go in the rest of that box.
func findBoxLine(b *board) (Step, bool) {
	for line := range 18 {
		for d := uint8(1); d <= 9; d++ {
			ps := b.positions(line, d)
			if len(ps) < 2 {
				continue
			}
			box := cellUnits[ps[0]][2]
			if !allIn(ps, box) {
				continue
			}
			var elims []Candidate
			for _, c := range b.positions(box, d) {
				if !slices.Contains(ps, c) {
					elims = append(elims, Candidate{c, d})
				}
			}
			if len(elims) == 0 {
				continue
			}
			return Step{
				Technique:    BoxLine,
				Eliminations: elims,
				Cells:        ps,
				Units:        []int{line, box},
				Text: fmt.Sprintf("In %s, %d can go only inside %s (%s). So %d must be in that part of %s: %s.",
					unitName(line), d, unitName(box), cellList(ps), d, unitName(box), elimText(elims)),
			}, true
		}
	}
	return Step{}, false
}

// findNakedSubset: n cells in a house whose candidates, together, are exactly
// n digits. Those digits must fill those cells, so they leave the rest of the
// house.
func findNakedSubset(b *board, n int, t Technique) (Step, bool) {
	for _, u := range unitScanOrder {
		var cells []int
		for _, c := range units[u] {
			if k := popcount(b.cand[c]); b.g[c] == 0 && k >= 2 && k <= n {
				cells = append(cells, c)
			}
		}
		for combo := range combos(len(cells), n) {
			var m uint16
			subset := make([]int, n)
			for i, k := range combo {
				subset[i] = cells[k]
				m |= b.cand[cells[k]]
			}
			if popcount(m) != n {
				continue
			}
			var elims []Candidate
			for _, c := range units[u] {
				if b.g[c] != 0 || slices.Contains(subset, c) {
					continue
				}
				for _, d := range digitsOf(b.cand[c] & m) {
					elims = append(elims, Candidate{c, d})
				}
			}
			if len(elims) == 0 {
				continue
			}
			return Step{
				Technique:    t,
				Eliminations: elims,
				Cells:        subset,
				Units:        []int{u},
				Text: fmt.Sprintf("In %s, %s can only hold %s between them. So those digits can't go anywhere else in %s: %s.",
					unitName(u), cellList(subset), digitList(digitsOf(m)), unitName(u), elimText(elims)),
			}, true
		}
	}
	return Step{}, false
}

// findHiddenSubset: n digits that, within a house, can only go in the same n
// cells. Those cells must hold those digits, so every other candidate in them
// goes.
func findHiddenSubset(b *board, n int, t Technique) (Step, bool) {
	for _, u := range unitScanOrder {
		var digits []uint8
		var where []uint16 // bit j set: units[u][j] allows the digit
		for d := uint8(1); d <= 9; d++ {
			if b.placedIn(u, d) {
				continue
			}
			var m uint16
			for j, c := range units[u] {
				if b.g[c] == 0 && b.cand[c]&(1<<d) != 0 {
					m |= 1 << j
				}
			}
			if k := popcount(m); k >= 1 && k <= n {
				digits = append(digits, d)
				where = append(where, m)
			}
		}
		for combo := range combos(len(digits), n) {
			var pos, dm uint16
			ds := make([]uint8, n)
			for i, k := range combo {
				pos |= where[k]
				dm |= 1 << digits[k]
				ds[i] = digits[k]
			}
			if popcount(pos) != n {
				continue
			}
			var subset []int
			var elims []Candidate
			for j, c := range units[u] {
				if pos&(1<<j) == 0 {
					continue
				}
				subset = append(subset, c)
				for _, d := range digitsOf(b.cand[c] &^ dm) {
					elims = append(elims, Candidate{c, d})
				}
			}
			if len(elims) == 0 {
				continue
			}
			return Step{
				Technique:    t,
				Eliminations: elims,
				Cells:        subset,
				Units:        []int{u},
				Text: fmt.Sprintf("In %s, %s can go only in %s. So those cells hold exactly those digits: %s.",
					unitName(u), digitList(ds), cellList(subset), elimText(elims)),
			}, true
		}
	}
	return Step{}, false
}

// findFish covers X-Wing (n=2) and Swordfish (3). If, in n
// rows, a digit can only go in the same n columns, then those n rows put the
// digit in each of those columns, so the rest of each column can't have it.
// The same holds with rows and columns swapped.
func findFish(b *board, n int, t Technique) (Step, bool) {
	for d := uint8(1); d <= 9; d++ {
		bit := uint16(1) << d
		for _, byRow := range []bool{true, false} {
			var lines []int
			var masks []uint16
			for i := range 9 {
				u := i
				if !byRow {
					u = 9 + i
				}
				var m uint16
				for j, c := range units[u] {
					if b.g[c] == 0 && b.cand[c]&bit != 0 {
						m |= 1 << j
					}
				}
				if k := popcount(m); k >= 2 && k <= n {
					lines = append(lines, i)
					masks = append(masks, m)
				}
			}
			for combo := range combos(len(lines), n) {
				var cover, base uint16
				for _, k := range combo {
					cover |= masks[k]
					base |= 1 << lines[k]
				}
				if popcount(cover) != n {
					continue
				}
				cell := func(line, cross int) int {
					if byRow {
						return line*9 + cross
					}
					return cross*9 + line
				}
				var elims []Candidate
				var pattern []int
				for j := range 9 {
					if cover&(1<<j) == 0 {
						continue
					}
					for i := range 9 {
						c := cell(i, j)
						if b.g[c] != 0 || b.cand[c]&bit == 0 {
							continue
						}
						if base&(1<<i) != 0 {
							pattern = append(pattern, c)
						} else {
							elims = append(elims, Candidate{c, d})
						}
					}
				}
				if len(elims) == 0 {
					continue
				}
				baseKind, coverKind := "rows", "columns"
				if !byRow {
					baseKind, coverKind = coverKind, baseKind
				}
				slices.Sort(pattern)
				return Step{
					Technique:    t,
					Eliminations: elims,
					Cells:        pattern,
					Text: fmt.Sprintf("%s on %d: in %s %s, %d can go only in %s %s. Each of those %s needs a %d, and they can only take them from those %s, so %d can't go anywhere else in those %s: %s.",
						t, d, baseKind, indexList(base), d, coverKind, indexList(cover), baseKind, d, coverKind, d, coverKind, elimText(elims)),
				}, true
			}
		}
	}
	return Step{}, false
}

// findXYWing: a pivot cell with candidates {a, b} sees two pincers, {a, c} and
// {b, c}. Whichever digit the pivot takes, one pincer becomes c, so a cell
// that sees both pincers can't be c.
func findXYWing(b *board) (Step, bool) {
	for p := range 81 {
		if b.g[p] != 0 || popcount(b.cand[p]) != 2 {
			continue
		}
		for _, x := range peers[p] {
			if b.g[x] != 0 || popcount(b.cand[x]) != 2 {
				continue
			}
			px := b.cand[x] & b.cand[p]
			cx := b.cand[x] &^ b.cand[p]
			if popcount(px) != 1 || popcount(cx) != 1 {
				continue
			}
			for _, y := range peers[p] {
				if y <= x || b.g[y] != 0 || popcount(b.cand[y]) != 2 {
					continue
				}
				py := b.cand[y] & b.cand[p]
				if popcount(py) != 1 || py == px || b.cand[y]&^b.cand[p] != cx {
					continue
				}
				c := digitsOf(cx)[0]
				elims := commonPeersWith(b, c, x, y)
				if len(elims) == 0 {
					continue
				}
				return Step{
					Technique:    XYWing,
					Eliminations: elims,
					Cells:        []int{p},
					Alt:          []int{x, y},
					Text: fmt.Sprintf("XY-Wing: %s is %s. If it's %d, then %s (%s) is %d; if it's %d, then %s (%s) is %d. Either way one of those two is %d, so a cell that sees both can't be %d: %s.",
						CellName(p), orList(digitsOf(b.cand[p])),
						digitsOf(px)[0], CellName(x), orList(digitsOf(b.cand[x])), c,
						digitsOf(py)[0], CellName(y), orList(digitsOf(b.cand[y])), c,
						c, c, elimText(elims)),
				}, true
			}
		}
	}
	return Step{}, false
}

// findXYZWing: like XY-Wing, but the pivot also holds the shared digit z:
// {x, y, z} with pincers {x, z} and {y, z}. One of the three cells must be z,
// so a cell that sees all three can't be.
func findXYZWing(b *board) (Step, bool) {
	for p := range 81 {
		if b.g[p] != 0 || popcount(b.cand[p]) != 3 {
			continue
		}
		for _, x := range peers[p] {
			if b.g[x] != 0 || popcount(b.cand[x]) != 2 || b.cand[x]&^b.cand[p] != 0 {
				continue
			}
			for _, y := range peers[p] {
				if y <= x || b.g[y] != 0 || popcount(b.cand[y]) != 2 ||
					b.cand[y]&^b.cand[p] != 0 || b.cand[y] == b.cand[x] {
					continue
				}
				z := digitsOf(b.cand[x] & b.cand[y])[0]
				var elims []Candidate
				for _, c := range commonPeersWith(b, z, x, y) {
					if isPeer[p][c.Cell] {
						elims = append(elims, c)
					}
				}
				if len(elims) == 0 {
					continue
				}
				return Step{
					Technique:    XYZWing,
					Eliminations: elims,
					Cells:        []int{p},
					Alt:          []int{x, y},
					Text: fmt.Sprintf("XYZ-Wing: %s is %s, and %s (%s) and %s (%s) both see it. However the three are filled, one of them is %d, so a cell that sees all three can't be %d: %s.",
						CellName(p), orList(digitsOf(b.cand[p])),
						CellName(x), orList(digitsOf(b.cand[x])), CellName(y), orList(digitsOf(b.cand[y])),
						z, z, elimText(elims)),
				}, true
			}
		}
	}
	return Step{}, false
}

// commonPeersWith returns the eliminations of d from every cell that sees both
// x and y.
func commonPeersWith(b *board, d uint8, x, y int) []Candidate {
	var out []Candidate
	for _, c := range peers[x] {
		if c != y && isPeer[y][c] && b.g[c] == 0 && b.cand[c]&(1<<d) != 0 {
			out = append(out, Candidate{c, d})
		}
	}
	slices.SortFunc(out, func(a, b Candidate) int { return a.Cell - b.Cell })
	return out
}

// findSimpleColouring follows one digit through houses where it has exactly
// two places. Each such pair is a strong link: one of the two cells is the
// digit. Linked cells chain into two alternating groups, A and B, and exactly
// one group holds the digit. Two rules follow:
//
//   - Two cells of the same group in one house: that group can't be the
//     digit, so remove the digit from every cell in it.
//   - A cell outside the chain that sees an A cell and a B cell: whichever
//     group is right, the cell sees the digit, so it can't hold it.
func findSimpleColouring(b *board) (Step, bool) {
	for d := uint8(1); d <= 9; d++ {
		var adj [81][]int
		for u := range 27 {
			if ps := b.positions(u, d); len(ps) == 2 && !b.placedIn(u, d) {
				adj[ps[0]] = append(adj[ps[0]], ps[1])
				adj[ps[1]] = append(adj[ps[1]], ps[0])
			}
		}
		var colour [81]int8
		for start := range 81 {
			if len(adj[start]) == 0 || colour[start] != 0 {
				continue
			}
			// Breadth-first two-colouring of one chain: 1 = A, 2 = B.
			var group [3][]int
			colour[start] = 1
			queue := []int{start}
			for len(queue) > 0 {
				c := queue[0]
				queue = queue[1:]
				group[colour[c]] = append(group[colour[c]], c)
				for _, n := range adj[c] {
					if colour[n] == 0 {
						colour[n] = 3 - colour[c]
						queue = append(queue, n)
					}
				}
			}
			a, bb := group[1], group[2]
			slices.Sort(a)
			slices.Sort(bb)
			if len(a)+len(bb) < 4 {
				continue // one link alone is a pointing pair or nothing
			}

			for k := 1; k <= 2; k++ {
				if x, y, ok := clash(group[k]); ok {
					elims := make([]Candidate, 0, len(group[k]))
					for _, c := range group[k] {
						elims = append(elims, Candidate{c, d})
					}
					slices.SortFunc(elims, func(p, q Candidate) int { return p.Cell - q.Cell })
					name := "A"
					if k == 2 {
						name = "B"
					}
					return Step{
						Technique:    SimpleColouring,
						Eliminations: elims,
						Cells:        a,
						Alt:          bb,
						Text: fmt.Sprintf("Simple colouring on %d: in each house along this chain, %d has exactly two places, so the shaded cells alternate between two groups, A and B, and one group holds every %d. Two %s cells, %s and %s, share a house, so group %s can't be %d: %s.",
							d, d, d, name, CellName(x), CellName(y), name, d, elimText(elims)),
					}, true
				}
			}

			var elims []Candidate
			for c := range 81 {
				if b.g[c] != 0 || b.cand[c]&(1<<d) == 0 || colour[c] != 0 {
					continue
				}
				if seesAny(c, a) && seesAny(c, bb) {
					elims = append(elims, Candidate{c, d})
				}
			}
			if len(elims) > 0 {
				return Step{
					Technique:    SimpleColouring,
					Eliminations: elims,
					Cells:        a,
					Alt:          bb,
					Text: fmt.Sprintf("Simple colouring on %d: in each house along this chain, %d has exactly two places, so the shaded cells alternate between two groups, A and B, and one group holds every %d. A cell that sees both an A cell and a B cell can't be %d: %s.",
						d, d, d, d, elimText(elims)),
				}, true
			}
		}
	}
	return Step{}, false
}

func clash(cells []int) (int, int, bool) {
	for i, x := range cells {
		for _, y := range cells[i+1:] {
			if isPeer[x][y] {
				return x, y, true
			}
		}
	}
	return 0, 0, false
}

func seesAny(c int, cells []int) bool {
	return slices.ContainsFunc(cells, func(o int) bool { return isPeer[c][o] })
}

func allIn(cells []int, u int) bool {
	for _, c := range cells {
		if !slices.Contains(cellUnits[c][:], u) {
			return false
		}
	}
	return true
}

// combos yields every k-element subset of 0..n-1 in lexicographic order. The
// yielded slice is reused; copy it to keep it.
func combos(n, k int) iter.Seq[[]int] {
	return func(yield func([]int) bool) {
		if k > n || k <= 0 {
			return
		}
		idx := make([]int, k)
		for i := range idx {
			idx[i] = i
		}
		for {
			if !yield(idx) {
				return
			}
			i := k - 1
			for i >= 0 && idx[i] == n-k+i {
				i--
			}
			if i < 0 {
				return
			}
			idx[i]++
			for j := i + 1; j < k; j++ {
				idx[j] = idx[j-1] + 1
			}
		}
	}
}

// Text helpers. Lists use a serial comma: "1, 2, and 3".

func joinList(items []string, conj string) string {
	switch len(items) {
	case 0:
		return ""
	case 1:
		return items[0]
	case 2:
		return items[0] + " " + conj + " " + items[1]
	}
	return strings.Join(items[:len(items)-1], ", ") + ", " + conj + " " + items[len(items)-1]
}

func cellList(cells []int) string {
	s := make([]string, len(cells))
	for i, c := range cells {
		s[i] = CellName(c)
	}
	return joinList(s, "and")
}

func digitStrings(ds []uint8) []string {
	s := make([]string, len(ds))
	for i, d := range ds {
		s[i] = fmt.Sprint(d)
	}
	return s
}

func digitList(ds []uint8) string { return joinList(digitStrings(ds), "and") }
func orList(ds []uint8) string    { return joinList(digitStrings(ds), "or") }

// indexList names the set bits of m, 1-based: 0b101 → "1 and 3".
func indexList(m uint16) string {
	var s []string
	for i := range 9 {
		if m&(1<<i) != 0 {
			s = append(s, fmt.Sprint(i+1))
		}
	}
	return joinList(s, "and")
}

// elimText phrases a set of eliminations. One digit reads "remove 4 from R1C1
// and R2C2"; several read "remove 5 and 9 from R1C4, and 6 from R2C6".
func elimText(elims []Candidate) string {
	byDigit := map[uint8][]int{}
	for _, e := range elims {
		byDigit[e.Digit] = append(byDigit[e.Digit], e.Cell)
	}
	if len(byDigit) == 1 {
		for d, cells := range byDigit {
			return fmt.Sprintf("remove %d from %s", d, cellList(cells))
		}
	}
	var order []int
	byCell := map[int][]uint8{}
	for _, e := range elims {
		if _, seen := byCell[e.Cell]; !seen {
			order = append(order, e.Cell)
		}
		byCell[e.Cell] = append(byCell[e.Cell], e.Digit)
	}
	parts := make([]string, len(order))
	for i, c := range order {
		parts[i] = fmt.Sprintf("%s from %s", digitList(byCell[c]), CellName(c))
	}
	return "remove " + joinList(parts, "and")
}
