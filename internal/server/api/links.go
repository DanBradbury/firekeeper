package api

import (
	"errors"
	"net/url"
	"strings"

	"github.com/DanBradbury/firekeeper/internal/server/store"
)

// linkFields are the placeholders a file link template may use.
var linkFields = []string{"{project}", "{ref}", "{commit}", "{branch}", "{path}"}

// ValidateFileLink checks a file link template such as
// "https://github.com/me/{project}/blob/{ref}/{path}". It must be an
// absolute http or https URL and use {path}.
func ValidateFileLink(tmpl string) error {
	if !strings.Contains(tmpl, "{path}") {
		return errors.New("file link template must contain {path}")
	}
	probe := tmpl
	for _, f := range linkFields {
		probe = strings.ReplaceAll(probe, f, "x")
	}
	u, err := url.Parse(probe)
	if err != nil || (u.Scheme != "http" && u.Scheme != "https") || u.Host == "" {
		return errors.New("file link template must be an absolute http or https URL")
	}
	return nil
}

// fileLink fills tmpl for one file of se. It returns "" when the template
// is unset, the path is absolute or redacted, or a placeholder the template
// uses has no value. {ref} is the commit when known, else the branch.
func fileLink(tmpl string, se store.Session, f store.SessionFile) string {
	if tmpl == "" || f.Absolute || strings.Contains(f.Path, "[REDACTED:") {
		return ""
	}
	segs := strings.Split(f.Path, "/")
	for i, s := range segs {
		segs[i] = url.PathEscape(s)
	}
	ref := se.Commit
	if ref == "" {
		ref = se.Branch
	}
	values := map[string]string{
		"{project}": url.PathEscape(se.Project),
		"{ref}":     url.PathEscape(ref),
		"{commit}":  url.PathEscape(se.Commit),
		"{branch}":  url.PathEscape(se.Branch),
		"{path}":    strings.Join(segs, "/"),
	}
	out := tmpl
	for _, k := range linkFields {
		if !strings.Contains(out, k) {
			continue
		}
		if values[k] == "" {
			return ""
		}
		out = strings.ReplaceAll(out, k, values[k])
	}
	return out
}
