package cmd

import (
	"io"
	"os"
)

// stdout and stderr are indirected through these helpers so tests can
// replace the streams without exec'ing the binary.
var (
	stdoutWriter io.Writer = os.Stdout
	stderrWriter io.Writer = os.Stderr
)

func stdout() io.Writer { return stdoutWriter }
func stderr() io.Writer { return stderrWriter }
