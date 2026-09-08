//go:build !windows

package updater

import "syscall"

// oNoFollow makes an open refuse a symlink at the target path. Combined with
// O_EXCL it keeps the staging file from being written through something
// another user planted where the new binary is about to land.
const oNoFollow = syscall.O_NOFOLLOW
