package redact_test

import (
	"strings"
	"testing"

	"github.com/DanBradbury/firekeeper/pkg/event"
	"github.com/DanBradbury/firekeeper/pkg/redact"
)

func TestPublicEntryPoints(t *testing.T) {
	opts := redact.Options{HomeDir: "/home/dev", Paths: []string{"/srv/private"}}

	got, counts := redact.String("see /srv/private/x and /home/dev/src", opts)
	if got != "see [REDACTED:path]/x and ~/src" || counts.Total() != 2 {
		t.Fatalf("String = %q (%d replacements)", got, counts.Total())
	}

	in := event.Event{
		Provider: event.ProviderCodex, Role: event.RoleUser, Text: "/srv/private/plan.md",
		Raw: []byte(`{"cwd":"/home/dev/src"}`),
	}
	out, _ := redact.Event(in, opts)
	if strings.Contains(out.Text, "private") || string(out.Raw) != `{"cwd":"~/src"}` {
		t.Fatalf("Event = %q %s", out.Text, out.Raw)
	}
	if in.Text != "/srv/private/plan.md" {
		t.Fatal("Event modified its input")
	}
}
