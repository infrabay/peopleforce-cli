//go:build !unix

package command

// oNoFollow is unavailable here; installSkill's Lstat checks are the only
// guard against a symlinked destination.
const oNoFollow = 0

func isSymlinkLoop(error) bool { return false }
