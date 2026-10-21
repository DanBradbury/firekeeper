//go:build !(darwin || linux || freebsd || openbsd || netbsd || dragonfly)

package reporter

// tryLock is a no-op where flock is unavailable. State writes stay atomic,
// but concurrent passes can overwrite each other's offsets; the server's
// idempotency keeps that from losing or duplicating events.
func tryLock(string) (unlock func(), ok bool, err error) {
	return func() {}, true, nil
}
