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
	"n":    count,
	"time": func(t interface{ Format(string) string }) string { return t.Format("Jan 2 15:04 MST") },
	"day":  func(t interface{ Format(string) string }) string { return t.Format("Mon Jan 2") },
	"pct":  func(f float64) string { return fmt.Sprintf("%.1f%%", f) },
	"bar": func(part, total int64) template.CSS {
		if total <= 0 {
			return "width:0"
		}
		return template.CSS(fmt.Sprintf("width:%.1f%%", float64(part)/float64(total)*100))
	},
	"deref": func(f *float64) float64 { return *f },
}).Parse(`<!doctype html>
<html lang="en"><head><meta charset="utf-8"><meta name="viewport" content="width=device-width,initial-scale=1">
<title>{{.Period.Label}} window: {{.Provider}}</title>
<style>
:root{--bg:#fff;--fg:#1c1b1a;--mute:#6b6762;--line:#e4e1dc;--bar:#d9641e}
@media (prefers-color-scheme:dark){:root{--bg:#161514;--fg:#ece9e4;--mute:#9a958e;--line:#2d2b29;--bar:#f08a4b}}
body{margin:0;padding:24px 16px;background:var(--bg);color:var(--fg);font:15px/1.5 system-ui,sans-serif}
main{max-width:900px;margin:0 auto}h1{font-size:22px;margin:0 0 4px}h2{font-size:16px;margin:32px 0 8px}
.mute{color:var(--mute)}table{width:100%;border-collapse:collapse;font-variant-numeric:tabular-nums}
th,td{text-align:left;padding:6px 8px;border-bottom:1px solid var(--line)}th{font-weight:600;color:var(--mute);font-size:13px}
td.r,th.r{text-align:right}.bar{height:6px;background:var(--bar);border-radius:3px;min-width:1px}
.wrap{overflow-x:auto}
</style></head><body><main>
<h1>{{.Provider}}: {{.Period.Label}} window</h1>
<p class="mute">{{time .Start}} to {{time .End}}{{if eq .Basis "rolling"}} (rolling {{.Period.Label}} window; no reset time known){{else}} (ends at the provider's reset){{end}}
{{if .UsedPercent}} · provider reports {{pct (deref .UsedPercent)}} used{{end}}</p>
<p><strong>{{n .Total.Total}}</strong> tokens · {{n .Total.Input}} input · {{n .Total.Output}} output · {{n .Total.Cache}} cache · {{len .Sessions}} sessions</p>
{{range .Notes}}<p class="mute">Note: {{.}}</p>{{end}}
<h2>Sessions</h2><div class="wrap"><table><tr><th>Session</th><th>Project</th><th>Active</th><th class="r">Turns</th><th class="r">Tokens</th><th class="r">Share</th><th style="width:20%"></th></tr>
{{range .Sessions}}<tr><td>{{.Title}}</td><td>{{.Project}}</td><td>{{time .FirstAt}} to {{.LastAt.Format "15:04"}}</td><td class="r">{{.Turns}}</td><td class="r">{{n .Tokens.Total}}</td><td class="r">{{pct .SharePct}}</td><td><div class="bar" style="{{bar .Tokens.Total $.Total.Total}}"></div></td></tr>
{{else}}<tr><td colspan="7" class="mute">No activity in this window.</td></tr>{{end}}</table></div>
<h2>By model</h2><table>{{range .ByModel}}<tr><td>{{.Name}}</td><td class="r">{{n .Tokens.Total}}</td></tr>{{else}}<tr><td class="mute">None</td></tr>{{end}}</table>
<h2>By project</h2><table>{{range .ByProject}}<tr><td>{{.Name}}</td><td class="r">{{n .Tokens.Total}}</td></tr>{{else}}<tr><td class="mute">None</td></tr>{{end}}</table>
{{if .Hourly}}<h2>By hour</h2><table>{{range .Hourly}}<tr><td>{{time .Start}}</td><td class="r">{{n .Tokens.Total}}</td><td style="width:50%"><div class="bar" style="{{bar .Tokens.Total $.Total.Total}}"></div></td></tr>{{end}}</table>{{end}}
{{if .Daily}}<h2>By day</h2><table>{{range .Daily}}<tr><td>{{day .Start}}</td><td class="r">{{n .Tokens.Total}}</td><td style="width:50%"><div class="bar" style="{{bar .Tokens.Total $.Total.Total}}"></div></td></tr>{{end}}</table>{{end}}
<p class="mute">Local transcripts only; token totals can be incomplete and are not the provider's billing figure. Generated {{time .GeneratedAt}}.</p>
</main></body></html>`))

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
