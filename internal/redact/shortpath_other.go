//go:build !windows

package redact

// osShortPathName returns "": only Windows has 8.3 short names to look up.
func osShortPathName(string) string { return "" }
