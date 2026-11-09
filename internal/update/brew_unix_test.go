//go:build unix

package update

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// fakeBrew is a stand-in for Homebrew's behaviour around a third-party tap.
// State lives in files under $FAKE_BREW_DIR:
//
//	remote  the formula version on GitHub
//	formula the formula version in the local clone of the tap
//	linked  the installed version linked into the prefix
//
// `brew update` copies remote to formula (unless update_fails exists);
// `brew upgrade` links formula, or, like Homebrew, warns "already installed"
// and exits 0 when formula is already linked (unless upgrade_fails exists).
const fakeBrew = `#!/bin/sh
d="$FAKE_BREW_DIR"
echo "$*" >> "$d/calls"
case "$1" in
update)
	if [ -e "$d/update_fails" ]; then echo "Error: Fetching /opt/homebrew/Library/Taps/other/homebrew-broken failed!" >&2; exit 1; fi
	cp "$d/remote" "$d/formula"
	echo "==> Updated 1 tap (danbradbury/tap)."
	;;
upgrade)
	if [ -e "$d/upgrade_fails" ]; then echo "Error: Cannot write to /opt/homebrew/Cellar" >&2; exit 1; fi
	if [ "$(cat "$d/formula")" = "$(cat "$d/linked")" ]; then
		echo "Warning: danbradbury/tap/firekeeper $(cat "$d/linked") already installed" >&2
		exit 0
	fi
	cp "$d/formula" "$d/linked"
	;;
info)
	printf '{"formulae":[{"name":"firekeeper","versions":{"stable":"%s"},"linked_keg":"%s"}],"casks":[]}\n' "$(cat "$d/formula")" "$(cat "$d/linked")"
	;;
*)
	echo "fake brew: unexpected $*" >&2
	exit 2
	;;
esac
`

type brewSetup struct {
	remote, formula, linked   string
	updateFails, upgradeFails bool
	script                    string // replaces fakeBrew when set
}

// installFakeBrew puts fakeBrew first on PATH and returns its state directory.
func installFakeBrew(t *testing.T, s brewSetup) string {
	t.Helper()
	bin := t.TempDir()
	script := fakeBrew
	if s.script != "" {
		script = s.script
	}
	if err := os.WriteFile(filepath.Join(bin, "brew"), []byte(script), 0o755); err != nil {
		t.Fatal(err)
	}
	state := t.TempDir()
	write := func(name, content string) {
		if err := os.WriteFile(filepath.Join(state, name), []byte(content), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	write("remote", s.remote)
	write("formula", s.formula)
	write("linked", s.linked)
	if s.updateFails {
		write("update_fails", "")
	}
	if s.upgradeFails {
		write("upgrade_fails", "")
	}
	t.Setenv("FAKE_BREW_DIR", state)
	t.Setenv("PATH", bin+string(os.PathListSeparator)+os.Getenv("PATH"))
	return state
}

func readState(t *testing.T, dir, name string) string {
	t.Helper()
	b, err := os.ReadFile(filepath.Join(dir, name))
	if err != nil {
		t.Fatal(err)
	}
	return strings.TrimSpace(string(b))
}

func TestInstallHomebrew(t *testing.T) {
	tests := []struct {
		name       string
		setup      brewSetup
		wantErr    []string // substrings of the error; nil means success
		wantLinked string
		wantShown  []string // substrings of what is written to errOut
	}{
		{
			name:       "tap already current",
			setup:      brewSetup{remote: "0.10.3", formula: "0.10.3", linked: "0.10.2"},
			wantLinked: "0.10.3",
		},
		{
			// The reported bug: the local tap predates the release, and brew's
			// auto-update did not run, so `brew upgrade` alone exited 0 with
			// nothing done and Firekeeper said it had updated.
			name:       "stale local tap is refreshed first",
			setup:      brewSetup{remote: "0.10.3", formula: "0.10.2", linked: "0.10.2"},
			wantLinked: "0.10.3",
		},
		{
			name:       "an unrelated tap failing to update is not fatal",
			setup:      brewSetup{remote: "0.10.3", formula: "0.10.3", linked: "0.10.2", updateFails: true},
			wantLinked: "0.10.3",
		},
		{
			name:       "brew upgrade exits 0 without upgrading",
			setup:      brewSetup{remote: "0.10.2", formula: "0.10.2", linked: "0.10.2"},
			wantErr:    []string{"still at 0.10.2", "brew update"},
			wantLinked: "0.10.2",
			wantShown:  []string{"0.10.2 already installed"},
		},
		{
			name:       "stale tap that cannot be refreshed",
			setup:      brewSetup{remote: "0.10.3", formula: "0.10.2", linked: "0.10.2", updateFails: true},
			wantErr:    []string{"still at 0.10.2"},
			wantLinked: "0.10.2",
			wantShown:  []string{"homebrew-broken failed", "0.10.2 already installed"},
		},
		{
			name:       "brew upgrade fails",
			setup:      brewSetup{remote: "0.10.3", formula: "0.10.3", linked: "0.10.2", upgradeFails: true},
			wantErr:    []string{"exit status 1"},
			wantLinked: "0.10.2",
			wantShown:  []string{"Cannot write to"},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			state := installFakeBrew(t, tt.setup)
			exe := filepath.Join(t.TempDir(), "Cellar", "firekeeper", "0.10.2", "bin", "firekeeper")
			var out, errOut strings.Builder
			err := Install(context.Background(), "0.10.3", exe, &out, &errOut)

			if got := readState(t, state, "calls"); !strings.HasPrefix(got, "update\nupgrade DanBradbury/tap/firekeeper") {
				t.Errorf("brew calls = %q, want update, then upgrade", got)
			}
			if got := readState(t, state, "linked"); got != tt.wantLinked {
				t.Errorf("linked = %s, want %s", got, tt.wantLinked)
			}
			if tt.wantErr == nil {
				if err != nil {
					t.Fatalf("Install = %v; brew said %q", err, errOut.String())
				}
				if errOut.Len() != 0 {
					t.Errorf("successful upgrade printed brew output: %q", errOut.String())
				}
				return
			}
			if err == nil {
				t.Fatal("Install reported success, but the new version is not linked")
			}
			for _, s := range tt.wantErr {
				if !strings.Contains(err.Error(), s) {
					t.Errorf("error %q does not mention %q", err, s)
				}
			}
			for _, s := range tt.wantShown {
				if !strings.Contains(errOut.String(), s) {
					t.Errorf("brew output %q not shown; got %q", s, errOut.String())
				}
			}
		})
	}
}

func TestInstallHomebrewUnreadableInfo(t *testing.T) {
	// brew info answers with something that is not JSON.
	script := strings.Replace(fakeBrew, `printf '{"formulae"`, `echo 'Error: unknown' #`, 1)
	if script == fakeBrew {
		t.Fatal("fake brew script no longer has the info answer to replace")
	}
	state := installFakeBrew(t, brewSetup{remote: "0.10.3", formula: "0.10.3", linked: "0.10.2", script: script})
	exe := filepath.Join(t.TempDir(), "Cellar", "firekeeper", "0.10.2", "bin", "firekeeper")
	var out, errOut strings.Builder
	err := Install(context.Background(), "0.10.3", exe, &out, &errOut)
	if err == nil || !strings.Contains(err.Error(), "unconfirmed") {
		t.Fatalf("Install = %v, want an unconfirmed-version error", err)
	}
	if got := readState(t, state, "linked"); got != "0.10.3" {
		t.Fatalf("linked = %s, want the upgrade to have run", got)
	}
}
