package main

import (
	"math"
	"math/rand/v2"
	"sort"
)

// minCI is the smallest sample that gets a confidence interval; below it the
// report says the sample is too small instead of printing a range.
const minCI = 5

// minTest is the smallest per-group sample for a pairwise test.
const minTest = 5

// bootstrapB is the number of bootstrap resamples.
const bootstrapB = 4000

// Summary describes one metric over a set of runs.
type Summary struct {
	N      int     `json:"n"`
	Mean   float64 `json:"mean"`
	Median float64 `json:"median"`
	SD     float64 `json:"sd"`
	// CILo and CIHi are a 95% bootstrap percentile interval for the mean,
	// set only when N >= minCI.
	CILo  *float64 `json:"ci95_lo,omitempty"`
	CIHi  *float64 `json:"ci95_hi,omitempty"`
	Range [2]float64
}

func summarize(xs []float64, seed uint64) Summary {
	s := Summary{N: len(xs)}
	if len(xs) == 0 {
		return s
	}
	s.Mean = mean(xs)
	s.Median = median(xs)
	s.SD = stdev(xs)
	s.Range = [2]float64{slicesMin(xs), slicesMax(xs)}
	if len(xs) >= minCI {
		lo, hi := bootstrapMeanCI(xs, bootstrapB, seed)
		s.CILo, s.CIHi = &lo, &hi
	}
	return s
}

func mean(xs []float64) float64 {
	if len(xs) == 0 {
		return 0
	}
	var s float64
	for _, x := range xs {
		s += x
	}
	return s / float64(len(xs))
}

func median(xs []float64) float64 {
	if len(xs) == 0 {
		return 0
	}
	c := append([]float64(nil), xs...)
	sort.Float64s(c)
	n := len(c)
	if n%2 == 1 {
		return c[n/2]
	}
	return (c[n/2-1] + c[n/2]) / 2
}

// stdev is the sample standard deviation (n-1 denominator).
func stdev(xs []float64) float64 {
	if len(xs) < 2 {
		return 0
	}
	m := mean(xs)
	var ss float64
	for _, x := range xs {
		ss += (x - m) * (x - m)
	}
	return math.Sqrt(ss / float64(len(xs)-1))
}

func slicesMin(xs []float64) float64 {
	m := xs[0]
	for _, x := range xs {
		m = math.Min(m, x)
	}
	return m
}

func slicesMax(xs []float64) float64 {
	m := xs[0]
	for _, x := range xs {
		m = math.Max(m, x)
	}
	return m
}

// percentile of sorted xs, linear interpolation.
func percentile(sorted []float64, p float64) float64 {
	if len(sorted) == 0 {
		return 0
	}
	pos := p * float64(len(sorted)-1)
	lo := int(math.Floor(pos))
	hi := int(math.Ceil(pos))
	return sorted[lo] + (sorted[hi]-sorted[lo])*(pos-float64(lo))
}

// bootstrapMeanCI is a seeded 95% percentile interval for the mean.
func bootstrapMeanCI(xs []float64, b int, seed uint64) (lo, hi float64) {
	rng := rand.New(rand.NewPCG(seed, 0x5bd1e995))
	means := make([]float64, b)
	n := len(xs)
	for i := range means {
		var s float64
		for range n {
			s += xs[rng.IntN(n)]
		}
		means[i] = s / float64(n)
	}
	sort.Float64s(means)
	return percentile(means, 0.025), percentile(means, 0.975)
}

// bootstrapDiffCI is a seeded 95% percentile interval for mean(b) - mean(a).
func bootstrapDiffCI(a, b []float64, iters int, seed uint64) (lo, hi float64) {
	rng := rand.New(rand.NewPCG(seed, 0x9e3779b9))
	diffs := make([]float64, iters)
	for i := range diffs {
		var sa, sb float64
		for range a {
			sa += a[rng.IntN(len(a))]
		}
		for range b {
			sb += b[rng.IntN(len(b))]
		}
		diffs[i] = sb/float64(len(b)) - sa/float64(len(a))
	}
	sort.Float64s(diffs)
	return percentile(diffs, 0.025), percentile(diffs, 0.975)
}

// MWU is a two-sided Mann-Whitney U test result.
type MWU struct {
	// U1 counts pairs where the a value is larger (ties count one half);
	// U2 = n1*n2 - U1.
	U1, U2 float64
	Z      float64
	P      float64
}

// mannWhitney runs the two-sided Mann-Whitney U test with the normal
// approximation, a tie correction to the variance and a continuity
// correction. It is rough for small samples; callers flag n < minTest.
func mannWhitney(a, b []float64) MWU {
	n1, n2 := len(a), len(b)
	if n1 == 0 || n2 == 0 {
		return MWU{P: 1}
	}
	type obs struct {
		v     float64
		fromA bool
	}
	all := make([]obs, 0, n1+n2)
	for _, v := range a {
		all = append(all, obs{v, true})
	}
	for _, v := range b {
		all = append(all, obs{v, false})
	}
	sort.Slice(all, func(i, j int) bool { return all[i].v < all[j].v })
	n := n1 + n2
	var r1, tieSum float64
	for i := 0; i < n; {
		j := i
		for j < n && all[j].v == all[i].v {
			j++
		}
		rank := float64(i+1+j) / 2 // average of ranks i+1..j
		for k := i; k < j; k++ {
			if all[k].fromA {
				r1 += rank
			}
		}
		t := float64(j - i)
		tieSum += t*t*t - t
		i = j
	}
	u1 := r1 - float64(n1*(n1+1))/2
	u2 := float64(n1*n2) - u1
	mu := float64(n1*n2) / 2
	nf := float64(n)
	variance := float64(n1*n2) / 12 * ((nf + 1) - tieSum/(nf*(nf-1)))
	res := MWU{U1: u1, U2: u2, P: 1}
	if variance <= 0 {
		return res
	}
	u := math.Min(u1, u2)
	z := math.Min(0, u-mu+0.5) / math.Sqrt(variance)
	res.Z = z
	res.P = math.Min(1, math.Erfc(-z/math.Sqrt2))
	return res
}

// holm returns Holm-Bonferroni adjusted p-values in the input order.
func holm(ps []float64) []float64 {
	m := len(ps)
	idx := make([]int, m)
	for i := range idx {
		idx[i] = i
	}
	sort.SliceStable(idx, func(i, j int) bool { return ps[idx[i]] < ps[idx[j]] })
	adj := make([]float64, m)
	running := 0.0
	for rank, i := range idx {
		v := math.Min(1, float64(m-rank)*ps[i])
		running = math.Max(running, v)
		adj[i] = running
	}
	return adj
}
