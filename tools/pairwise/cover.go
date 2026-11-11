package main

// Greedy pairwise covering array (AETG style) with constraints.
//
// Every pair of values of two different factors that some valid row can
// hold must appear in at least one row. Pairs no valid row can hold (the
// constraints exclude them) are counted and skipped. Rows are built one at
// a time: several candidates per row, each started from a value that occurs
// in the most uncovered pairs, the other factors filled in a shuffled order
// with the value covering the most new pairs, never a value that leaves the
// row unable to satisfy the constraints. The best candidate is kept. A PCG
// generator seeded from the model's seed makes the output deterministic.

import (
	"fmt"
	"math/rand/v2"
)

const candidatesPerRow = 30

type coverStats struct {
	Values   int // total candidate values over all factors
	Pairs    int // pairs to cover (feasible)
	Excluded int // pairs the constraints make impossible
}

type pairKey struct{ fi, vi, fj, vj int }

func mkPair(fi, vi, fj, vj int) pairKey {
	if fi > fj {
		fi, vi, fj, vj = fj, vj, fi, vi
	}
	return pairKey{fi, vi, fj, vj}
}

// completable reports whether the partial row (-1 = unassigned) can be
// extended to a full row that violates no constraint.
func (cm *compiledModel) completable(row []int) bool {
	if cm.violates(row) {
		return false
	}
	for fi := range row {
		if row[fi] >= 0 {
			continue
		}
		for vi := range cm.Factors[fi].Values {
			row[fi] = vi
			if cm.completable(row) {
				row[fi] = -1
				return true
			}
		}
		row[fi] = -1
		return false
	}
	return true
}

func emptyRow(n int) []int {
	r := make([]int, n)
	for i := range r {
		r[i] = -1
	}
	return r
}

// cover builds the covering array of cm. Each row holds a value index per factor.
func cover(cm *compiledModel) ([][]int, coverStats, error) {
	n := len(cm.Factors)
	var st coverStats
	for _, f := range cm.Factors {
		st.Values += len(f.Values)
	}
	if !cm.completable(emptyRow(n)) {
		return nil, st, fmt.Errorf("the constraints exclude every configuration")
	}
	uncovered := map[pairKey]bool{}
	var order []pairKey // deterministic iteration
	for fi := 0; fi < n; fi++ {
		for fj := fi + 1; fj < n; fj++ {
			for vi := range cm.Factors[fi].Values {
				for vj := range cm.Factors[fj].Values {
					row := emptyRow(n)
					row[fi], row[fj] = vi, vj
					p := pairKey{fi, vi, fj, vj}
					if cm.completable(row) {
						uncovered[p] = true
						order = append(order, p)
					} else {
						st.Excluded++
					}
				}
			}
		}
	}
	st.Pairs = len(uncovered)
	seed := cm.Model.Seed
	rng := rand.New(rand.NewPCG(seed, seed^0x9e3779b97f4a7c15))

	var rows [][]int
	for len(uncovered) > 0 {
		var best []int
		bestGain := -1
		for c := 0; c < candidatesPerRow; c++ {
			row := buildCandidate(cm, uncovered, order, rng)
			if row == nil {
				continue
			}
			g := gain(row, uncovered)
			if g > bestGain {
				best, bestGain = row, g
			}
		}
		if best == nil || bestGain <= 0 {
			return nil, st, fmt.Errorf("internal: no row covers any of the %d remaining pairs", len(uncovered))
		}
		for fi := 0; fi < n; fi++ {
			for fj := fi + 1; fj < n; fj++ {
				delete(uncovered, pairKey{fi, best[fi], fj, best[fj]})
			}
		}
		rows = append(rows, best)
	}
	return rows, st, nil
}

func gain(row []int, uncovered map[pairKey]bool) int {
	g := 0
	for fi := range row {
		for fj := fi + 1; fj < len(row); fj++ {
			if uncovered[pairKey{fi, row[fi], fj, row[fj]}] {
				g++
			}
		}
	}
	return g
}

func buildCandidate(cm *compiledModel, uncovered map[pairKey]bool, order []pairKey, rng *rand.Rand) []int {
	n := len(cm.Factors)
	// Start from the factor value occurring in the most uncovered pairs
	// (ties broken at random), seeded with one of its uncovered pairs.
	count := map[[2]int]int{}
	for _, p := range order {
		if uncovered[p] {
			count[[2]int{p.fi, p.vi}]++
			count[[2]int{p.fj, p.vj}]++
		}
	}
	bestN := 0
	var tops [][2]int
	for fi := 0; fi < n; fi++ {
		for vi := range cm.Factors[fi].Values {
			k := [2]int{fi, vi}
			switch c := count[k]; {
			case c > bestN:
				bestN, tops = c, [][2]int{k}
			case c == bestN && c > 0:
				tops = append(tops, k)
			}
		}
	}
	if len(tops) == 0 {
		return nil
	}
	start := tops[rng.IntN(len(tops))]
	var partners []pairKey
	for _, p := range order {
		if uncovered[p] && ((p.fi == start[0] && p.vi == start[1]) || (p.fj == start[0] && p.vj == start[1])) {
			partners = append(partners, p)
		}
	}
	first := partners[rng.IntN(len(partners))]
	row := emptyRow(n)
	row[first.fi], row[first.fj] = first.vi, first.vj

	rest := make([]int, 0, n)
	for fi := 0; fi < n; fi++ {
		if row[fi] < 0 {
			rest = append(rest, fi)
		}
	}
	rng.Shuffle(len(rest), func(i, j int) { rest[i], rest[j] = rest[j], rest[i] })
	for _, fi := range rest {
		bestV, bestG := -1, -1
		var ties []int
		for vi := range cm.Factors[fi].Values {
			row[fi] = vi
			if !cm.completable(row) {
				continue
			}
			g := 0
			for fj := 0; fj < n; fj++ {
				if fj != fi && row[fj] >= 0 && uncovered[mkPair(fi, vi, fj, row[fj])] {
					g++
				}
			}
			switch {
			case g > bestG:
				bestG, ties = g, []int{vi}
			case g == bestG:
				ties = append(ties, vi)
			}
		}
		if len(ties) == 0 {
			return nil
		}
		bestV = ties[rng.IntN(len(ties))]
		row[fi] = bestV
	}
	return row
}
