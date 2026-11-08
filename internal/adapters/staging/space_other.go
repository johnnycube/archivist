//go:build !(linux || darwin || freebsd || openbsd || dragonfly)

package staging

// freeBytes is unknown on this platform; CheckSpace is skipped.
func freeBytes(string) (uint64, bool) { return 0, false }
