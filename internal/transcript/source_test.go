package transcript

import (
	"testing"

	"github.com/DanBradbury/firekeeper/internal/session"
)

type stubSource struct{}

func (stubSource) Locate(session.Meta) ([]string, error) { return nil, nil }

func (stubSource) Read(string, int64) ([]Event, int64, error) { return nil, 0, nil }

func TestRegistry(t *testing.T) {
	const provider Provider = "registry-test"
	if _, ok := For(provider); ok {
		t.Fatal("unregistered provider found")
	}
	Register(provider, stubSource{})
	source, ok := For(provider)
	if !ok || source == nil {
		t.Fatal("registered source not found")
	}
	found := false
	for _, p := range Registered() {
		found = found || p == provider
	}
	if !found {
		t.Fatalf("Registered() = %v, missing %q", Registered(), provider)
	}

	assertPanics(t, "duplicate", func() { Register(provider, stubSource{}) })
	assertPanics(t, "nil", func() { Register("registry-test-nil", nil) })
}

func assertPanics(t *testing.T, name string, fn func()) {
	t.Helper()
	defer func() {
		if recover() == nil {
			t.Errorf("%s: expected panic", name)
		}
	}()
	fn()
}
