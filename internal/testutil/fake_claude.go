package testutil

import (
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"sync"
	"testing"
)

// fakeClaudeSource is a tiny Go program that acts as a stand-in for the
// `claude` CLI. On each invocation it reads a JSON config file located at
// <os.Executable()>.config.json and echoes the scripted stdout/stderr/exit.
//
// Putting the config next to the binary (one copy per test) means multiple
// FakeClaude handles can exist concurrently without stepping on each other —
// the stub never consults environment state.
const fakeClaudeSource = `package main

import (
	"encoding/json"
	"os"
)

type config struct {
	Stdout string ` + "`json:\"stdout\"`" + `
	Stderr string ` + "`json:\"stderr\"`" + `
	Exit   int    ` + "`json:\"exit\"`" + `
}

func main() {
	exe, err := os.Executable()
	if err != nil {
		os.Stderr.WriteString("fake-claude: Executable: " + err.Error() + "\n")
		os.Exit(2)
	}
	data, err := os.ReadFile(exe + ".config.json")
	if err != nil {
		os.Stderr.WriteString("fake-claude: read config: " + err.Error() + "\n")
		os.Exit(2)
	}
	var c config
	if err := json.Unmarshal(data, &c); err != nil {
		os.Stderr.WriteString("fake-claude: parse config: " + err.Error() + "\n")
		os.Exit(2)
	}
	if c.Stdout != "" {
		os.Stdout.WriteString(c.Stdout)
	}
	if c.Stderr != "" {
		os.Stderr.WriteString(c.Stderr)
	}
	os.Exit(c.Exit)
}
`

var (
	fakeClaudeOnce  sync.Once
	fakeClaudeCache string
	fakeClaudeErr   error
)

// FakeClaude is a per-test handle to a compiled `claude` stub. Tests pass
// FakeClaude.BinDir to PrependPath (or prepend it themselves) so the code
// under test resolves "claude" to the stub. SetResponse configures what the
// next invocation will print and exit with.
type FakeClaude struct {
	// BinDir is the directory containing the stub binary, named "claude".
	// Prepend it to PATH so subprocesses resolve "claude" to this stub.
	BinDir string
	// BinPath is the absolute path to the stub binary.
	BinPath string
	// ConfigPath is the JSON file the stub reads on each invocation.
	ConfigPath string
}

// NewFakeClaude compiles the stub (once per test binary), copies it into a
// per-test directory as "claude", and returns a handle. The caller controls
// the stub's behavior through SetResponse. Calling PrependPath wires the
// stub into PATH for the current test.
func NewFakeClaude(t *testing.T) *FakeClaude {
	t.Helper()
	cached, err := buildFakeClaude()
	if err != nil {
		t.Fatalf("testutil: compile fake claude: %v", err)
	}
	dir := t.TempDir()
	name := "claude"
	if runtime.GOOS == "windows" {
		name = "claude.exe"
	}
	dst := filepath.Join(dir, name)
	if err := copyExecutable(cached, dst); err != nil {
		t.Fatalf("testutil: copy fake claude: %v", err)
	}
	fc := &FakeClaude{
		BinDir:     dir,
		BinPath:    dst,
		ConfigPath: dst + ".config.json",
	}
	// Seed a default response so a misconfigured test produces a useful error
	// instead of an exit(2) "read config" message.
	if err := fc.SetResponse(`{"error":"FakeClaude.SetResponse was not called"}`, "", 1); err != nil {
		t.Fatalf("testutil: seed fake claude: %v", err)
	}
	return fc
}

// SetResponse writes the scripted response the stub will emit on its next
// invocation. Overwrites any prior response.
func (fc *FakeClaude) SetResponse(stdout, stderr string, exit int) error {
	cfg := struct {
		Stdout string `json:"stdout"`
		Stderr string `json:"stderr"`
		Exit   int    `json:"exit"`
	}{stdout, stderr, exit}
	data, err := json.Marshal(cfg)
	if err != nil {
		return fmt.Errorf("marshal fake-claude config: %w", err)
	}
	if err := os.WriteFile(fc.ConfigPath, data, 0o644); err != nil {
		return fmt.Errorf("write fake-claude config: %w", err)
	}
	return nil
}

// PrependPath inserts BinDir at the front of PATH for the remainder of the
// test. Uses t.Setenv, so the test cannot also call t.Parallel.
func (fc *FakeClaude) PrependPath(t *testing.T) {
	t.Helper()
	t.Setenv("PATH", fc.BinDir+string(os.PathListSeparator)+os.Getenv("PATH"))
}

// buildFakeClaude compiles the stub once per test binary and caches the
// resulting executable in a package-level temp dir. Returns the cached path.
func buildFakeClaude() (string, error) {
	fakeClaudeOnce.Do(func() {
		dir, err := os.MkdirTemp("", "devlog-fake-claude-")
		if err != nil {
			fakeClaudeErr = fmt.Errorf("tempdir: %w", err)
			return
		}
		src := filepath.Join(dir, "main.go")
		if err := os.WriteFile(src, []byte(fakeClaudeSource), 0o644); err != nil {
			fakeClaudeErr = fmt.Errorf("write source: %w", err)
			return
		}
		out := filepath.Join(dir, "claude")
		if runtime.GOOS == "windows" {
			out += ".exe"
		}
		cmd := exec.Command("go", "build", "-o", out, src)
		cmd.Env = append(os.Environ(), "CGO_ENABLED=0")
		if output, err := cmd.CombinedOutput(); err != nil {
			fakeClaudeErr = fmt.Errorf("go build fake-claude: %v: %s", err, output)
			return
		}
		fakeClaudeCache = out
	})
	return fakeClaudeCache, fakeClaudeErr
}

func copyExecutable(src, dst string) error {
	data, err := os.ReadFile(src)
	if err != nil {
		return err
	}
	return os.WriteFile(dst, data, 0o755)
}
