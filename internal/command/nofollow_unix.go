//go:build unix

package command

import (
	"errors"
	"syscall"
)

// oNoFollow makes the final open of installSkill fail on a symlink instead of
// following it.
const oNoFollow = syscall.O_NOFOLLOW

func isSymlinkLoop(err error) bool { return errors.Is(err, syscall.ELOOP) }
