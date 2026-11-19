package main

import (
	"fmt"
	"html/template"
	"io"
	"math"
	"strings"
)

// The HTML page is self-contained: inline CSS and SVG, no scripts, no
// external requests.

type hBar struct {
	Label, Color                  string
	Y                             float64
	X0, W                         float64 // bar
	Lo, Hi                        float64 // whisker x positions, 0 when none
	Text                          string
	FreshW, CachedW, WriteW, OutW float64
}

type hDot struct {
	X, Y  float64
	Color string
	Title string
}

type hGroup struct {
	Title, Notice, Small string
	H                    float64
	Hit, Total, Reason   []hBar
	Stack                []hBar
	Legend               []hBar
	Dots                 []hDot
	XTicks               []hTick
	Rows                 []Row
	Pairs                []Pair
	Heat                 []hHeatRow
	HeatCols             []string
	Gaps, Hours          []GapBin
}

type hTick struct {
	X     float64
	Label string
}

type hHeatRow struct {
	Effort string
	Cells  []hHeat
}

type hHeat struct {
	Text, Style string
}

const (
	barX     = 110.0
	barWidth = 420.0
)

func (rep Report) writeHTML(w io.Writer) error {
	var groups []hGroup
	for _, g := range rep.Groups {
		groups = append(groups, htmlGroup(g, rep.Runs))
	}
	return htmlPage.Execute(w, map[string]any{
		"Groups": groups, "N": len(rep.Runs), "Batches": len(rep.Batches), "Notes": rep.Notes,
		"Generated": rep.GeneratedAt.Local().Format("Jan 2 2006 15:04 MST"),
	})
}

func htmlGroup(g Group, runs []Result) hGroup {
	hg := hGroup{Title: g.Harness + " · " + emptyOr(g.Model, "unknown model"), Rows: g.Rows, Pairs: g.Pairs, Gaps: g.GapBins, Hours: g.HourBins}
	if g.Kind != "run" {
		hg.Title += " · warm repeats"
	}
	if len(g.NotLogged) > 0 {
		hg.Notice = "Not logged by " + g.Harness + " (n/a): " + strings.Join(g.NotLogged, ", ") + ". Compare it as this harness's cost for the task; its system prompt and tools differ."
	}
	if len(g.Small) > 0 {
		hg.Small = "Too few runs for intervals or tests: " + strings.Join(g.Small, ", ") + "."
	}
	efforts := make([]string, len(g.Rows))
	for i, r := range g.Rows {
		efforts[i] = r.Effort
	}
	hg.H = float64(len(g.Rows))*30 + 10
	var maxTotal, maxStack, maxReason float64
	for _, r := range g.Rows {
		maxTotal = math.Max(maxTotal, ciHi(r.Total))
		maxStack = math.Max(maxStack, r.Total.Mean)
		if r.Reasoning != nil {
			maxReason = math.Max(maxReason, ciHi(*r.Reasoning))
		}
	}
	scale := func(v, m float64) float64 {
		if m <= 0 {
			return 0
		}
		return barWidth * v / m
	}
	for i, r := range g.Rows {
		y := 6 + float64(i)*30
		color := effortColor(efforts, r.Effort)
		hg.Hit = append(hg.Hit, whiskerBar(r.Effort, color, y, r.CacheHit, 100, fmt.Sprintf("%.1f%%", r.CacheHit.Mean)))
		hg.Total = append(hg.Total, whiskerBar(r.Effort, color, y, r.Total, maxTotal, comma(int64(r.Total.Mean))))
		if r.Reasoning != nil {
			hg.Reason = append(hg.Reason, whiskerBar(r.Effort, color, y, *r.Reasoning, maxReason, comma(int64(r.Reasoning.Mean))))
		}
		write := 0.0
		if r.CacheWrite != nil {
			write = *r.CacheWrite
		}
		hg.Stack = append(hg.Stack, hBar{Label: r.Effort, Y: y,
			FreshW: scale(r.Fresh, maxStack), CachedW: scale(r.Cached, maxStack), WriteW: scale(write, maxStack), OutW: scale(r.Output.Mean, maxStack),
			Text: fmt.Sprintf("%s (%s fresh, %s read, %s write, %s out)", comma(int64(r.Total.Mean)), comma(int64(r.Fresh)), comma(int64(r.Cached)), comma(int64(write)), comma(int64(r.Output.Mean)))})
		hg.Legend = append(hg.Legend, hBar{Label: r.Effort, Color: color})
	}

	// Scatter: cache hit % against the gap since the previous run, log x.
	const sx, sw, sy, sh = 50.0, 560.0, 10.0, 200.0
	minLog, maxLog := math.Log10(5), math.Log10(6*3600)
	xOf := func(sec float64) float64 {
		l := math.Log10(math.Max(5, math.Min(sec, 6*3600)))
		return sx + sw*(l-minLog)/(maxLog-minLog)
	}
	for _, t := range []struct {
		s     float64
		label string
	}{{10, "10s"}, {60, "1m"}, {300, "5m"}, {1800, "30m"}, {7200, "2h"}, {21600, "6h+"}} {
		hg.XTicks = append(hg.XTicks, hTick{X: xOf(t.s), Label: t.label})
	}
	for _, r := range runs {
		if !r.ok() || r.GapPrevSec == nil || r.Harness != g.Harness || emptyOr(r.Model, r.ModelRequested) != g.Model || r.Kind != g.Kind {
			continue
		}
		hg.Dots = append(hg.Dots, hDot{
			X: xOf(*r.GapPrevSec), Y: sy + sh*(1-r.CacheHitPct()/100), Color: effortColor(efforts, r.Effort),
			Title: fmt.Sprintf("%s: %.1f%% after %s", r.Effort, r.CacheHitPct(), fmtGap(r.GapPrevSec)),
		})
	}

	// Heat table: cached % per request number.
	cols := map[string]bool{}
	for _, q := range g.PerRequest {
		cols[q.Index] = true
	}
	for k := 1; k <= maxReqIndex+1; k++ {
		label := fmt.Sprint(k)
		if k == maxReqIndex+1 {
			label += "+"
		}
		if cols[label] {
			hg.HeatCols = append(hg.HeatCols, label)
		}
	}
	for _, e := range efforts {
		row := hHeatRow{Effort: e}
		for _, c := range hg.HeatCols {
			cell := hHeat{Text: "", Style: ""}
			for _, q := range g.PerRequest {
				if q.Effort == e && q.Index == c {
					// White at 100% cached, red as the miss share grows.
					miss := math.Max(0, math.Min(1, 1-q.CachedShare/100))
					cell = hHeat{
						Text:  fmt.Sprintf("%.0f%% · %.0f%% of misses", q.CachedShare, q.UncachedShare),
						Style: fmt.Sprintf("background:rgba(228,87,46,%.2f)", 0.08+0.85*miss),
					}
				}
			}
			row.Cells = append(row.Cells, cell)
		}
		hg.Heat = append(hg.Heat, row)
	}
	return hg
}

func ciHi(s Summary) float64 {
	if s.CIHi != nil {
		return math.Max(*s.CIHi, s.Mean)
	}
	return s.Mean
}

func whiskerBar(label, color string, y float64, s Summary, maxV float64, text string) hBar {
	b := hBar{Label: label, Color: color, Y: y, X0: barX, Text: text}
	if maxV <= 0 {
		return b
	}
	b.W = barWidth * s.Mean / maxV
	if s.CILo != nil {
		b.Lo = barX + barWidth*math.Max(0, *s.CILo)/maxV
		b.Hi = barX + barWidth*math.Min(maxV, *s.CIHi)/maxV
	}
	return b
}

var htmlPage = template.Must(template.New("bench").Funcs(template.FuncMap{
	"f1": func(f float64) string { return fmt.Sprintf("%.1f", f) },
	"n":  func(f float64) string { return comma(int64(math.Round(f))) },
	"p":  fmtP,
	"ci": fmtSummary,
	"add": func(a ...float64) float64 {
		var s float64
		for _, v := range a {
			s += v
		}
		return s
	},
	"optN": func(f *float64) string {
		if f == nil {
			return "n/a"
		}
		return comma(int64(*f))
	},
	"optS": func(s *Summary) string {
		if s == nil {
			return "n/a"
		}
		return fmtSummary(*s, 0)
	},
}).Parse(`<!doctype html><html><head><meta charset="utf-8"><title>Reasoning effort vs tokens and cache</title>
<style>body{font:14px -apple-system,system-ui,sans-serif;margin:2rem auto;max-width:1040px;color:#222;padding:0 1rem}
h1{font-size:20px}h2{font-size:17px;margin-top:2.5rem;border-top:1px solid #ddd;padding-top:1rem}h3{font-size:14px;margin-top:1.5rem}
svg text{font:11px -apple-system,system-ui,sans-serif}.k span{display:inline-block;width:10px;height:10px;margin:0 4px 0 12px}
table{border-collapse:collapse;font-size:12px;margin:.5rem 0}td,th{padding:3px 7px;border-bottom:1px solid #eee;text-align:right}
td:first-child,th:first-child{text-align:left}.mute{color:#777}.warn{color:#a33}</style></head><body>
<h1>Reasoning effort vs tokens and cache</h1>
<p>{{.N}} stored runs from {{.Batches}} batch(es). Same prompt and repo state, fresh session per run. Bars are means; whiskers are 95% bootstrap intervals.</p>
{{range .Groups}}<h2>{{.Title}}</h2>
{{if .Notice}}<p class="warn">{{.Notice}}</p>{{end}}{{if .Small}}<p class="warn">{{.Small}}</p>{{end}}
<h3>Cache hit % (cache read ÷ input)</h3>
<svg width="980" height="{{.H}}" role="img">{{range .Hit}}<text x="0" y="{{add .Y 15}}">{{.Label}}</text>
<rect x="110" y="{{.Y}}" width="420" height="20" fill="#eee"/><rect x="110" y="{{.Y}}" width="{{.W}}" height="20" fill="{{.Color}}"/>
{{if .Hi}}<line x1="{{.Lo}}" x2="{{.Hi}}" y1="{{add .Y 10}}" y2="{{add .Y 10}}" stroke="#222"/><line x1="{{.Lo}}" x2="{{.Lo}}" y1="{{add .Y 5}}" y2="{{add .Y 15}}" stroke="#222"/><line x1="{{.Hi}}" x2="{{.Hi}}" y1="{{add .Y 5}}" y2="{{add .Y 15}}" stroke="#222"/>{{end}}
<text x="540" y="{{add .Y 15}}">{{.Text}}</text>{{end}}</svg>
<h3>Total tokens per run</h3>
<svg width="980" height="{{.H}}" role="img">{{range .Total}}<text x="0" y="{{add .Y 15}}">{{.Label}}</text>
<rect x="110" y="{{.Y}}" width="{{.W}}" height="20" fill="{{.Color}}"/>
{{if .Hi}}<line x1="{{.Lo}}" x2="{{.Hi}}" y1="{{add .Y 10}}" y2="{{add .Y 10}}" stroke="#222"/><line x1="{{.Lo}}" x2="{{.Lo}}" y1="{{add .Y 5}}" y2="{{add .Y 15}}" stroke="#222"/><line x1="{{.Hi}}" x2="{{.Hi}}" y1="{{add .Y 5}}" y2="{{add .Y 15}}" stroke="#222"/>{{end}}
<text x="540" y="{{add .Y 15}}">{{.Text}}</text>{{end}}</svg>
<h3>Where the tokens go (means)</h3>
<div class="k"><span style="background:#e4572e"></span>fresh input<span style="background:#4c9be8"></span>cache read<span style="background:#f2a541"></span>cache write<span style="background:#2bb673"></span>output (incl. reasoning)</div>
<svg width="980" height="{{.H}}" role="img">{{range .Stack}}<text x="0" y="{{add .Y 15}}">{{.Label}}</text>
<rect x="110" y="{{.Y}}" width="{{.FreshW}}" height="20" fill="#e4572e"/><rect x="{{add 110 .FreshW}}" y="{{.Y}}" width="{{.CachedW}}" height="20" fill="#4c9be8"/>
<rect x="{{add 110 .FreshW .CachedW}}" y="{{.Y}}" width="{{.WriteW}}" height="20" fill="#f2a541"/><rect x="{{add 110 .FreshW .CachedW .WriteW}}" y="{{.Y}}" width="{{.OutW}}" height="20" fill="#2bb673"/>
<text x="540" y="{{add .Y 15}}">{{.Text}}</text>{{end}}</svg>
{{if .Reason}}<h3>Reasoning output tokens</h3>
<svg width="980" height="{{.H}}" role="img">{{range .Reason}}<text x="0" y="{{add .Y 15}}">{{.Label}}</text>
<rect x="110" y="{{.Y}}" width="{{.W}}" height="20" fill="{{.Color}}"/>
{{if .Hi}}<line x1="{{.Lo}}" x2="{{.Hi}}" y1="{{add .Y 10}}" y2="{{add .Y 10}}" stroke="#222"/>{{end}}
<text x="540" y="{{add .Y 15}}">{{.Text}}</text>{{end}}</svg>{{else}}<p class="mute">Reasoning tokens: n/a for this harness.</p>{{end}}
<h3>Cache hit % against time since the previous run</h3>
<div class="k">{{range .Legend}}<span style="background:{{.Color}}"></span>{{.Label}}{{end}}</div>
<svg width="640" height="240" role="img"><rect x="50" y="10" width="560" height="200" fill="#fafafa" stroke="#ddd"/>
<text x="20" y="14">100%</text><text x="26" y="114">50%</text><text x="32" y="212">0%</text>
{{range .XTicks}}<line x1="{{.X}}" x2="{{.X}}" y1="10" y2="210" stroke="#e6e6e6"/><text x="{{add .X -8}}" y="226">{{.Label}}</text>{{end}}
{{range .Dots}}<circle cx="{{.X}}" cy="{{.Y}}" r="4" fill="{{.Color}}" fill-opacity="0.75"><title>{{.Title}}</title></circle>{{end}}
</svg>
<table><tr><th>gap</th><th>runs</th><th>cache hit %</th><th>1st request cached %</th><th>cold</th></tr>{{range .Gaps}}{{if .N}}<tr><td>{{.Label}}</td><td>{{.N}}</td><td>{{f1 .CacheHit}}</td><td>{{f1 .FirstCached}}</td><td>{{.Cold}}</td></tr>{{end}}{{end}}</table>
<table><tr><th>local time</th><th>runs</th><th>cache hit %</th><th>1st request cached %</th><th>cold</th></tr>{{range .Hours}}{{if .N}}<tr><td>{{.Label}}</td><td>{{.N}}</td><td>{{f1 .CacheHit}}</td><td>{{f1 .FirstCached}}</td><td>{{.Cold}}</td></tr>{{end}}{{end}}</table>
<h3>Cached % by request number (and share of all uncached input)</h3>
<table><tr><th>effort</th>{{range .HeatCols}}<th>#{{.}}</th>{{end}}</tr>{{range .Heat}}<tr><td>{{.Effort}}</td>{{range .Cells}}<td style="{{.Style}}">{{.Text}}</td>{{end}}</tr>{{end}}</table>
<h3>Statistics</h3>
<table><tr><th>effort</th><th>n</th><th>cache hit % [95% CI]</th><th>median</th><th>sd</th><th>total [95% CI]</th><th>sd</th><th>output</th><th>reasoning</th><th>wall s</th><th>cache write</th><th>requests</th><th>later-miss runs</th><th>tests</th></tr>
{{range .Rows}}<tr><td>{{.Effort}}</td><td>{{.N}}</td><td>{{ci .CacheHit 1}}</td><td>{{f1 .CacheHit.Median}}</td><td>{{f1 .CacheHit.SD}}</td><td>{{ci .Total 0}}</td><td>{{n .Total.SD}}</td><td>{{ci .Output 0}}</td><td>{{optS .Reasoning}}</td><td>{{ci .Wall 0}}</td><td>{{optN .CacheWrite}}</td><td>{{f1 .Requests}}</td><td>{{.LaterMiss}}</td><td>{{.Passed}}/{{.N}}</td></tr>{{end}}</table>
{{if .Pairs}}<h3>Pairwise: Mann-Whitney U (two-sided), Holm-corrected per metric</h3>
<table><tr><th>metric</th><th>a → b</th><th>n</th><th>mean diff (b−a)</th><th>95% CI</th><th>U</th><th>p</th><th>p (Holm)</th><th></th></tr>
{{range .Pairs}}<tr><td>{{.Metric}}</td><td>{{.A}} → {{.B}}</td><td>{{.NA}}/{{.NB}}</td><td>{{f1 .Diff}}</td><td>{{f1 .DiffLo}} to {{f1 .DiffHi}}</td><td>{{f1 .U}}</td><td>{{p .P}}</td><td>{{p .PHolm}}</td><td>{{if not .Sufficient}}n too small{{else if lt .PHolm 0.05}}<b>significant</b>{{else}}not significant{{end}}</td></tr>{{end}}</table>{{end}}
{{end}}
{{range .Notes}}<p class="mute">note: {{.}}</p>{{end}}
<p class="mute">Read from local agent records. Token counts are what the harness logged, not a billing statement. Generated {{.Generated}}.</p>
</body></html>`))
