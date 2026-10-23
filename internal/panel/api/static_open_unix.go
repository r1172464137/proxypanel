//go:build unix

package api

import "syscall"

// openNonBlocking keeps open(2) from blocking on a FIFO inside a mispackaged
// dist directory. Only regular files are ever served, so the flag has no
// effect on the intended content.
const openNonBlocking = syscall.O_NONBLOCK
