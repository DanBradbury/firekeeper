//go:build !windows

package winterm

// Focus raises the terminal window hosting pids. It needs Windows.
func Focus(pids []int) (string, error) { return "", ErrUnsupported }
