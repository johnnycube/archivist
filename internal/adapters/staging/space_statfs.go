//go:build linux || darwin || freebsd || openbsd || dragonfly

package staging

import "golang.org/x/sys/unix"

// freeBytes reports the space available to unprivileged writers on the
// filesystem holding dir.
func freeBytes(dir string) (uint64, bool) {
	var st unix.Statfs_t
	if err := unix.Statfs(dir, &st); err != nil {
		return 0, false
	}
	return uint64(st.Bavail) * uint64(st.Bsize), true
}
