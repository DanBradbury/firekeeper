package window

import (
	"fmt"
	"html/template"
	"io"
)

// WriteHTML renders the report as one self-contained page: no scripts, no
// external requests. Working directories are left out; only project names show.
func WriteHTML(w io.Writer, r Report) error {
	return page.Execute(w, r)
}

var page = template.Must(template.New("window").Funcs(template.FuncMap{
	"n":        count,
	"time":     func(t interface{ Format(string) string }) string { return t.Format("Jan 2 15:04 MST") },
	"pct":      pct,
	"perTurn":  func(total int64, turns int) int64 { return total / int64(turns) },
	"share":    shareOf,
	"buckets":  chartCols,
	"groupsOf": groupRows,
	"bar":      barWidth,
	"deref":    func(f *float64) float64 { return *f },
}).Parse(`<!doctype html>
<html lang="en"><head><meta charset="utf-8"><meta name="viewport" content="width=device-width,initial-scale=1">
<title>{{.Period.Label}} window: {{.Provider}}</title>
<style>
:root{--bg:#fafafa;--raised:#f4f4f5;--fg:#171717;--fg2:#525252;--mute:#737373;--line:rgba(23,23,23,.08);--fill:rgba(23,23,23,.28);--accent:#c2500f;--ease:cubic-bezier(.22,1,.36,1)}
@media (prefers-color-scheme:dark){:root{--bg:#141415;--raised:#232325;--fg:#ededed;--fg2:#a1a1a6;--mute:#85858b;--line:rgba(237,237,237,.08);--fill:rgba(237,237,237,.3);--accent:#f08a4b}}
*{box-sizing:border-box}
body{margin:0;padding:48px 16px 64px;background:var(--bg);color:var(--fg);font:15px/22px system-ui,-apple-system,"Segoe UI",sans-serif;-webkit-font-smoothing:antialiased}
main{max-width:960px;margin:0 auto}
h1{font-size:28px;line-height:32px;font-weight:500;margin:0 0 8px}
h2{font-size:15px;line-height:20px;font-weight:500;margin:48px 0 12px}
.mute{color:var(--mute)}.sub{color:var(--fg2);margin:0}.note{color:var(--mute);font-size:13px;line-height:20px;margin:8px 0 0}
.mono,td.r,th.r,.stats dd,.col .v,.col .l{font-family:ui-monospace,"SF Mono",Menlo,Consolas,monospace;font-variant-numeric:tabular-nums}
.stats{display:flex;flex-wrap:wrap;gap:24px 48px;margin:32px 0 0}.stats div{min-width:96px}
.stats dt{color:var(--mute);font-size:13px;line-height:20px}.stats dd{margin:4px 0 0;font-size:20px;line-height:24px}.stats .big dd{font-size:32px;line-height:32px}
table{width:100%;border-collapse:collapse;font-variant-numeric:tabular-nums}
th,td{text-align:left;padding:8px 12px;border-bottom:1px solid var(--line);vertical-align:middle}
th{font-weight:400;color:var(--mute);font-size:13px;line-height:20px}
td.r,th.r{text-align:right;font-size:14px}
.bar{height:4px;background:var(--fill);border-radius:2px;min-width:1px}
.wrap{overflow-x:auto}
.chart{display:flex;gap:8px;align-items:stretch;height:200px}
.col{flex:1;min-width:0;display:flex;flex-direction:column;justify-content:flex-end;gap:4px;text-align:center}
.col .cb{flex:1;display:flex;align-items:flex-end;background:var(--raised);border-radius:6px;overflow:hidden}
.col .cb i{display:block;width:100%;background:var(--fill);border-radius:4px 4px 0 0;min-height:1px}
.col.top .cb i{background:var(--accent)}
.col .v{font-size:12px;line-height:16px;color:var(--fg2)}.col .l{font-size:12px;line-height:16px;color:var(--mute)}
tr.det{display:none}tr:has(.tg:checked)+tr.det{display:table-row}
tr.det>td{background:var(--raised);padding:16px 20px}
.tg{position:absolute;opacity:0}
label.s{cursor:pointer;transition:color 150ms var(--ease)}label.s:hover{color:var(--fg2)}
label.s:before{content:"\25B8";display:inline-block;width:16px;color:var(--mute)}
.tg:checked+label.s:before{content:"\25BE"}.tg:focus-visible+label.s{outline:2px solid var(--accent);outline-offset:2px;border-radius:4px}
.det h3{font-size:13px;line-height:20px;color:var(--mute);margin:0 0 8px;font-weight:400}
.cols{display:flex;gap:32px;flex-wrap:wrap}.cols>div{flex:1 1 260px}
.det table td,.det table th{padding:4px 8px}.det p{margin:8px 0 0;font-size:13px;line-height:20px}
footer{margin-top:64px;color:var(--mute);font-size:13px;line-height:20px;max-width:640px}
@media (max-width:560px){.stats .big{flex-basis:100%}.chart{height:160px}}
</style></head><body><main>
<h1>{{.Provider}}, {{.Period.Label}} window</h1>
<p class="sub">{{time .Start}} to {{time .End}}{{if eq .Basis "rolling"}} (rolling, no reset time known){{else}} (ends at the provider's reset){{end}}</p>
{{range .Notes}}<p class="note">{{.}}</p>{{end}}
<dl class="stats">
<div class="big"><dt>Tokens</dt><dd>{{n .Total.Total}}</dd></div>
{{if .UsedPercent}}<div><dt>Reported used</dt><dd>{{pct (deref .UsedPercent)}}</dd></div>{{end}}
<div><dt>Input</dt><dd>{{n .Total.Input}}</dd></div>
<div><dt>Output</dt><dd>{{n .Total.Output}}</dd></div>
<div><dt>Cache</dt><dd>{{n .Total.Cache}}</dd></div>
<div><dt>Sessions</dt><dd>{{len .Sessions}}</dd></div>
</dl>
{{if .Hourly}}<h2>By hour</h2>{{template "chart" (buckets .Hourly "15:04" .Total.Total)}}{{end}}
{{if .Daily}}<h2>By day</h2>{{template "chart" (buckets .Daily "Mon 2" .Total.Total)}}{{end}}
<h2>Sessions</h2><div class="wrap"><table><tr><th>Session</th><th>Project</th><th>Active</th><th class="r">Turns</th><th class="r">Tokens</th><th class="r">Share</th><th style="width:16%"></th></tr>
{{range $i, $s := .Sessions}}<tr><td><input type="checkbox" class="tg" id="s{{$i}}"><label class="s" for="s{{$i}}">{{.Title}}</label></td><td>{{.Project}}</td><td class="mute">{{time .FirstAt}} to {{.LastAt.Format "15:04"}}</td><td class="r">{{.Turns}}</td><td class="r">{{n .Tokens.Total}}</td><td class="r">{{pct .SharePct}}</td><td><div class="bar" style="{{bar .Tokens.Total $.Total.Total}}"></div></td></tr>
<tr class="det"><td colspan="7"><div class="cols">
<div><h3>Tokens</h3><table><tr><td>Input</td><td class="r">{{n .Tokens.Input}}</td><td class="r">{{share .Tokens.Input .Tokens.Total}}</td></tr><tr><td>Output</td><td class="r">{{n .Tokens.Output}}</td><td class="r">{{share .Tokens.Output .Tokens.Total}}</td></tr><tr><td>Cache</td><td class="r">{{n .Tokens.Cache}}</td><td class="r">{{share .Tokens.Cache .Tokens.Total}}</td></tr></table>
<p class="mute">{{.Turns}} turns, {{.ToolCalls}} tool calls{{if .Turns}}, {{n (perTurn .Tokens.Total .Turns)}} tokens/turn{{end}}</p></div>
<div><h3>Models</h3><table>{{range .ByModel}}<tr><td>{{.Name}}</td><td class="r">{{n .Tokens.Total}}</td><td class="r">{{share .Tokens.Total $s.Tokens.Total}}</td></tr>{{else}}<tr><td class="mute">None</td></tr>{{end}}</table></div>
<div><h3>Activity</h3><table>{{range .Activity}}<tr><td>{{time .Start}}</td><td class="r">{{n .Tokens.Total}}</td><td style="width:40%"><div class="bar" style="{{bar .Tokens.Total $s.Tokens.Total}}"></div></td></tr>{{else}}<tr><td class="mute">None</td></tr>{{end}}</table></div>
</div></td></tr>
{{else}}<tr><td colspan="7" class="mute">No activity in this window</td></tr>{{end}}</table></div>
<h2>By model</h2>{{template "groups" (groupsOf .ByModel .Total.Total)}}
<h2>By project</h2>{{template "groups" (groupsOf .ByProject .Total.Total)}}
<footer>Local transcripts only. Token totals can be incomplete and are not the provider's billing figure. Generated {{time .GeneratedAt}}.</footer>
</main></body></html>`))

func init() {
	template.Must(page.Parse(`{{define "chart"}}<div class="chart">{{range .}}<div class="col{{if .Top}} top{{end}}"><span class="v">{{n .Tokens}}<br>{{.Share}}</span><div class="cb"><i style="{{.Height}}"></i></div><span class="l">{{.Label}}</span></div>{{end}}</div>{{end}}
{{define "groups"}}<table>{{range .}}<tr><td>{{.Name}}</td><td class="r">{{n .Tokens}}</td><td class="r">{{.Share}}</td><td style="width:30%"><div class="bar" style="{{.Bar}}"></div></td></tr>{{else}}<tr><td class="mute">None</td></tr>{{end}}</table>{{end}}`))
}

type chartCol struct {
	Label  string
	Tokens int64
	Share  string
	Height template.CSS
	Top    bool // the busiest bucket carries the accent
}

// chartCols turns buckets into columns scaled to the busiest one.
func chartCols(bs []Bucket, layout string, total int64) []chartCol {
	var peak int64
	for _, b := range bs {
		peak = max(peak, b.Tokens.Total)
	}
	cols := make([]chartCol, len(bs))
	for i, b := range bs {
		cols[i] = chartCol{Label: b.Start.Format(layout), Tokens: b.Tokens.Total, Share: shareOf(b.Tokens.Total, total), Height: "height:0", Top: peak > 0 && b.Tokens.Total == peak}
		if peak > 0 {
			cols[i].Height = template.CSS(fmt.Sprintf("height:%.1f%%", float64(b.Tokens.Total)/float64(peak)*100))
		}
	}
	return cols
}

type groupRow struct {
	Name   string
	Tokens int64
	Share  string
	Bar    template.CSS
}

func groupRows(gs []Group, total int64) []groupRow {
	rows := make([]groupRow, len(gs))
	for i, g := range gs {
		rows[i] = groupRow{g.Name, g.Tokens.Total, shareOf(g.Tokens.Total, total), barWidth(g.Tokens.Total, total)}
	}
	return rows
}

func shareOf(part, total int64) string {
	if total <= 0 {
		return pct(0)
	}
	return pct(float64(part) / float64(total) * 100)
}

func barWidth(part, total int64) template.CSS {
	if total <= 0 {
		return "width:0"
	}
	return template.CSS(fmt.Sprintf("width:%.1f%%", float64(part)/float64(total)*100))
}

func pct(f float64) string { return fmt.Sprintf("%.1f%%", f) }

func count(n int64) string {
	s := fmt.Sprint(n)
	for i := len(s) - 3; i > 0; i -= 3 {
		s = s[:i] + "," + s[i:]
	}
	return s
}

// WriteText prints a short table of the busiest sessions.
func WriteText(w io.Writer, r Report) {
	end := "15:04 MST"
	if r.Period == PeriodWeek {
		end = "Jan 2 15:04 MST"
	}
	fmt.Fprintf(w, "%s %s window %s to %s\n", r.Provider, r.Period.Label(), r.Start.Local().Format("Jan 2 15:04"), r.End.Local().Format(end))
	if r.UsedPercent != nil {
		fmt.Fprintf(w, "provider reports %.0f%% used\n", *r.UsedPercent)
	}
	fmt.Fprintf(w, "%s tokens (%s in, %s out, %s cache) across %d sessions\n\n",
		count(r.Total.Total), count(r.Total.Input), count(r.Total.Output), count(r.Total.Cache), len(r.Sessions))
	for _, s := range r.Sessions {
		fmt.Fprintf(w, "%6.1f%%  %12s  %-16.16s  %s\n", s.SharePct, count(s.Tokens.Total), s.Project, s.Title)
	}
	for _, note := range r.Notes {
		fmt.Fprintf(w, "\nnote: %s\n", note)
	}
}
