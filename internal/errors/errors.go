// Package errors provides the structured DevlogError type used by every
// devlog subcommand and internal package.
//
// All user-visible errors follow the four-part philosophy from SPEC.md:
//
//  1. What failed:  devlog: error: <component>: <message>
//  2. Why it matters / the underlying cause
//  3. What to do:   the remediation text
//  4. Where to look: a pointer to errors.log or a relevant file
//
// The first three parts are carried on DevlogError itself. The fourth
// ("errors logged to: <path>") is the responsibility of the caller, which
// knows the .devlog directory for the current session.
package errors

import (
	"encoding/json"
	"fmt"
	"os"
	"strings"
	"time"
)

// DevlogError is the structured error type used across all devlog
// components. Zero values are valid but meaningless; always construct via
// New or Wrap.
type DevlogError struct {
	Component   string
	Message     string
	Cause       error
	Remediation string
}

// New returns a DevlogError without an underlying cause.
func New(component, message string) *DevlogError {
	return &DevlogError{Component: component, Message: message}
}

// Wrap returns a DevlogError that carries cause as its underlying error.
// Callers that pass a nil cause should use New instead.
func Wrap(component, message string, cause error) *DevlogError {
	return &DevlogError{Component: component, Message: message, Cause: cause}
}

// WithRemediation returns a copy of e with the Remediation field set.
// Using copy-on-write keeps DevlogError values safe to share between
// goroutines.
func (e *DevlogError) WithRemediation(text string) *DevlogError {
	if e == nil {
		return nil
	}
	clone := *e
	clone.Remediation = text
	return &clone
}

// Error returns the compact one-line form:
//
//	devlog: error: <component>: <message>
func (e *DevlogError) Error() string {
	if e == nil {
		return ""
	}
	return fmt.Sprintf("devlog: error: %s: %s", e.Component, e.Message)
}

// Unwrap returns the underlying cause so DevlogError composes with
// errors.Is and errors.As from the stdlib.
func (e *DevlogError) Unwrap() error {
	if e == nil {
		return nil
	}
	return e.Cause
}

// Format renders the error for display. When full is false the result is
// equivalent to Error(). When full is true the cause (if any) and the
// remediation (if any) are appended on their own lines — all four parts
// of the error philosophy are included.
func (e *DevlogError) Format(full bool) string {
	if e == nil {
		return ""
	}
	if !full {
		return e.Error()
	}
	var b strings.Builder
	b.WriteString(e.Error())
	if e.Cause != nil {
		b.WriteString("\n  cause: ")
		b.WriteString(e.Cause.Error())
	}
	if e.Remediation != "" {
		b.WriteString("\n\n")
		b.WriteString(strings.TrimRight(e.Remediation, "\n"))
	}
	return b.String()
}

// logEntry is the on-disk JSON shape written to errors.log. Keeping it
// private lets the storage format evolve without leaking into the public
// API of DevlogError.
type logEntry struct {
	TS          string `json:"ts"`
	Component   string `json:"component"`
	Message     string `json:"message"`
	Cause       string `json:"cause,omitempty"`
	Remediation string `json:"remediation,omitempty"`
}

// WriteToLog appends a single JSON line describing e to the file at path,
// creating the file with mode 0644 if it doesn't exist.
//
// The file is opened with O_APPEND|O_CREATE|O_WRONLY. Each call builds
// the full JSON payload plus its terminating newline in memory and issues
// one os.File.Write, so concurrent calls from multiple goroutines (or
// processes) never interleave on POSIX filesystems.
func (e *DevlogError) WriteToLog(path string) error {
	if e == nil {
		return nil
	}
	f, err := os.OpenFile(path, os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0o644)
	if err != nil {
		return err
	}
	defer f.Close()

	entry := logEntry{
		TS:          time.Now().UTC().Format(time.RFC3339Nano),
		Component:   e.Component,
		Message:     e.Message,
		Remediation: e.Remediation,
	}
	if e.Cause != nil {
		entry.Cause = e.Cause.Error()
	}

	data, err := json.Marshal(entry)
	if err != nil {
		return err
	}
	data = append(data, '\n')
	if _, err := f.Write(data); err != nil {
		return err
	}
	return nil
}
