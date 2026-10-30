//go:build !windows

package winterm

import (
	"errors"
	"testing"
)

func TestFocusIsUnsupportedOffWindows(t *testing.T) {
	if _, err := Focus([]int{1}); !errors.Is(err, ErrUnsupported) {
		t.Fatalf("err = %v", err)
	}
}
