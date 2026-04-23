package cmd

import (
	"crypto/rand"
	"encoding/hex"
	stderrors "errors"
	"flag"
	"fmt"
	"os"
	"path/filepath"
	"time"

	derrors "devlog/internal/errors"
	"devlog/internal/git"
	"devlog/internal/state"
)

// Init implements `devlog init`. It validates the project is a git repo,
// creates the .devlog directory, generates a session ID, and writes the
// initial state.json and default config.json.
//
// Running twice without --force is a safe no-op: the existing session_id
// is preserved so hooks that fire mid-session do not lose their seq
// counters. --force resets the session entirely (new ID, cleared
// counters, updated started_at).
func Init(args []string) int {
	fs := flag.NewFlagSet("init", flag.ContinueOnError)
	fs.SetOutput(stderr())
	force := fs.Bool("force", false, "regenerate session_id and reset counters even if state.json exists")
	projectDir := fs.String("project", "", "project root (defaults to current working directory)")
	if err := fs.Parse(args); err != nil {
		return 2
	}

	root, err := resolveProjectRoot(*projectDir)
	if err != nil {
		printErr(err)
		return 1
	}

	if err := git.CheckRepo(root); err != nil {
		printErr(err)
		return 1
	}

	devlogDir := filepath.Join(root, ".devlog")
	if err := os.MkdirAll(devlogDir, 0o755); err != nil {
		printErr(derrors.Wrap("init", fmt.Sprintf("create %s", devlogDir), err))
		return 1
	}

	statePath := filepath.Join(devlogDir, "state.json")
	configPath := filepath.Join(devlogDir, "config.json")

	newSession, err := writeInitialState(statePath, *force)
	if err != nil {
		printErr(err)
		return 1
	}

	if err := writeDefaultConfigIfMissing(configPath); err != nil {
		printErr(err)
		return 1
	}

	fmt.Fprintf(stdout(), "devlog: initialized %s\n", devlogDir)
	if newSession {
		fmt.Fprintf(stdout(), "devlog: started new session\n")
	} else {
		fmt.Fprintf(stdout(), "devlog: resumed existing session\n")
	}
	return 0
}

// resolveProjectRoot returns an absolute path for projectDir, defaulting
// to the current working directory when projectDir is empty.
func resolveProjectRoot(projectDir string) (string, error) {
	root := projectDir
	if root == "" {
		wd, err := os.Getwd()
		if err != nil {
			return "", derrors.Wrap("init", "resolve working directory", err)
		}
		root = wd
	}
	abs, err := filepath.Abs(root)
	if err != nil {
		return "", derrors.Wrap("init", fmt.Sprintf("resolve absolute path for %s", root), err)
	}
	return abs, nil
}

// writeInitialState creates or refreshes statePath. It returns true when
// a brand-new session was started (either the file did not exist or
// --force was set), false when an existing session was preserved.
func writeInitialState(statePath string, force bool) (bool, error) {
	existing, err := state.Load(statePath)
	switch {
	case err == nil:
		if !force && existing.SessionID != "" {
			return false, nil
		}
		// Fall through to regenerate.
	case os.IsNotExist(err):
		// Fall through to create.
	default:
		return false, derrors.Wrap("init", fmt.Sprintf("read %s", statePath), err)
	}

	sessionID, err := generateSessionID()
	if err != nil {
		return false, err
	}
	s := &state.State{
		SessionID: sessionID,
		StartedAt: time.Now().UTC().Format(time.RFC3339),
	}
	if err := state.Save(statePath, s); err != nil {
		return false, derrors.Wrap("init", fmt.Sprintf("write %s", statePath), err)
	}
	return true, nil
}

// writeDefaultConfigIfMissing writes a fresh default config.json only
// when one does not already exist. Overwriting would clobber tuning the
// operator has applied with `devlog config`.
func writeDefaultConfigIfMissing(configPath string) error {
	if _, err := os.Stat(configPath); err == nil {
		return nil
	} else if !os.IsNotExist(err) {
		return derrors.Wrap("init", fmt.Sprintf("stat %s", configPath), err)
	}
	if err := state.SaveConfig(configPath, state.Default()); err != nil {
		return err
	}
	return nil
}

// generateSessionID produces a 16-character lowercase hex string (64 bits
// of entropy). That is enough to distinguish concurrent sessions on a
// single workstation while staying short enough to show in status output.
func generateSessionID() (string, error) {
	buf := make([]byte, 8)
	if _, err := rand.Read(buf); err != nil {
		return "", derrors.Wrap("init", "generate session id", err)
	}
	return hex.EncodeToString(buf), nil
}

// printErr renders err to stderr in the four-part format when it is a
// DevlogError, or as a bare string otherwise.
func printErr(err error) {
	var de *derrors.DevlogError
	if stderrors.As(err, &de) {
		fmt.Fprintln(stderr(), de.Format(true))
		return
	}
	fmt.Fprintln(stderr(), err.Error())
}
