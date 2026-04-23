// Package git provides the git-related helpers used by devlog's capture
// and init paths. The helpers here only inspect the filesystem — they do
// NOT shell out to the git binary. Keeping CheckRepo shell-free means
// `devlog init` can produce the "no git repository found" error even on
// hosts that lack git in PATH.
package git

import (
	"fmt"
	"os"
	"path/filepath"

	devlogerrors "devlog/internal/errors"
)

// CheckRepo reports whether path (or any ancestor directory) contains a
// `.git` entry — either a directory (standard clone) or a regular file
// (worktree / submodule GIT_DIR pointer).
//
// On success it returns nil. On failure it returns a *DevlogError whose
// remediation matches the contract in SPEC.md:
//
//	devlog: error: no git repository found at /path/to/project
//
//	DevLog requires git to track code changes. Initialize a repository:
//
//	    cd /path/to/project && git init
//
//	Then re-run: devlog init
//
// An empty path is treated as the current working directory.
func CheckRepo(path string) error {
	if path == "" {
		wd, err := os.Getwd()
		if err != nil {
			return devlogerrors.Wrap("git", "resolve working directory", err)
		}
		path = wd
	}

	abs, err := filepath.Abs(path)
	if err != nil {
		return devlogerrors.Wrap("git", fmt.Sprintf("resolve absolute path for %s", path), err)
	}

	// Walk upward. The loop terminates because filepath.Dir on a root
	// returns the same root (e.g. "/" on unix, "C:\\" on windows), which
	// we detect explicitly.
	current := abs
	for {
		candidate := filepath.Join(current, ".git")
		info, statErr := os.Stat(candidate)
		switch {
		case statErr == nil:
			// Either `.git` is a directory (standard repo) or a file
			// whose contents point at the real GIT_DIR (worktree case).
			// Both are valid — the file form is produced by
			// `git worktree add` and `git submodule`.
			_ = info
			return nil
		case !os.IsNotExist(statErr):
			// Permission denied, stale NFS handle, etc. Surface it
			// rather than pretending the repo does not exist.
			return devlogerrors.Wrap("git", fmt.Sprintf("stat %s", candidate), statErr)
		}

		parent := filepath.Dir(current)
		if parent == current {
			break
		}
		current = parent
	}

	return devlogerrors.New("git",
		fmt.Sprintf("no git repository found at %s", abs)).
		WithRemediation(
			"DevLog requires git to track code changes. Initialize a repository:\n\n" +
				"    cd " + abs + " && git init\n\n" +
				"Then re-run: devlog init\n",
		)
}
