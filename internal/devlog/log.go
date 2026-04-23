package devlog

import (
	"bufio"
	"encoding/json"
	"fmt"
	"io"
	"os"

	devlogerrors "devlog/internal/errors"
)

const MaxScannerBytes = 4 * 1024 * 1024 // 4 MiB line cap for JSONL scanners

// Append writes entry as a single JSON line at the end of path, creating
// the file with mode 0644 if it does not yet exist.
//
// The implementation marshals the entry in memory, appends a trailing
// newline, and issues one os.File.Write under O_APPEND semantics. That
// guarantees no two concurrent Append calls interleave on POSIX
// filesystems — each call writes a whole record atomically. Concurrent
// readers that observe a partial flush therefore never see a half-written
// line.
func Append(path string, entry Entry) error {
	data, err := json.Marshal(entry)
	if err != nil {
		return devlogerrors.Wrap("devlog", "encode log entry", err)
	}
	data = append(data, '\n')

	f, err := os.OpenFile(path, os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0o644)
	if err != nil {
		return devlogerrors.Wrap("devlog", fmt.Sprintf("open %s", path), err)
	}
	defer f.Close()

	if _, err := f.Write(data); err != nil {
		return devlogerrors.Wrap("devlog", fmt.Sprintf("append to %s", path), err)
	}
	return nil
}

// ReadLastN returns up to n entries from the tail of path, in original
// file order (oldest-first among the returned slice). If the file holds
// fewer than n entries, all entries are returned. A missing file is
// treated as "no entries yet" and yields (nil, nil) — callers such as the
// summarizer prompt builder on a fresh session should not have to special
// case the first flush.
//
// The implementation streams through the file and keeps a ring buffer of
// size n. That avoids reading the whole log into memory, which matters
// once a long session accumulates hundreds of entries.
func ReadLastN(path string, n int) ([]Entry, error) {
	if n < 0 {
		return nil, devlogerrors.New("devlog",
			fmt.Sprintf("ReadLastN: n must be >= 0 (got %d)", n))
	}
	if n == 0 {
		return nil, nil
	}

	f, err := os.Open(path)
	if err != nil {
		if os.IsNotExist(err) {
			return nil, nil
		}
		return nil, devlogerrors.Wrap("devlog", fmt.Sprintf("open %s", path), err)
	}
	defer f.Close()

	// Ring buffer — at most n entries retained.
	ring := make([]Entry, n)
	count := 0
	head := 0

	scanner := bufio.NewScanner(f)
	// Lift the default 64KB line cap. Summaries are short but the
	// scanner should not silently truncate a malformed long line.
	scanner.Buffer(make([]byte, 0, 64*1024), MaxScannerBytes)

	lineNo := 0
	for scanner.Scan() {
		lineNo++
		raw := scanner.Bytes()
		if len(raw) == 0 {
			continue
		}
		var entry Entry
		if err := json.Unmarshal(raw, &entry); err != nil {
			return nil, devlogerrors.Wrap("devlog",
				fmt.Sprintf("decode %s line %d", path, lineNo), err).
				WithRemediation(
					"The log file appears corrupt. Inspect it with:\n\n" +
						"    sed -n '" + fmt.Sprintf("%dp", lineNo) + "' " + path + "\n\n" +
						"If only a tail line is bad you can truncate it manually.\n",
				)
		}
		ring[head] = entry
		head = (head + 1) % n
		count++
	}
	if err := scanner.Err(); err != nil {
		if err == io.EOF {
			// bufio.Scanner does not surface EOF, but guard anyway.
			err = nil
		} else {
			return nil, devlogerrors.Wrap("devlog", fmt.Sprintf("scan %s", path), err)
		}
	}

	if count == 0 {
		return nil, nil
	}

	size := count
	if size > n {
		size = n
	}
	out := make([]Entry, size)
	// The oldest-of-the-last-n entry sits at head when the ring has wrapped,
	// or at 0 when count < n.
	start := 0
	if count > n {
		start = head
	}
	for i := 0; i < size; i++ {
		out[i] = ring[(start+i)%n]
	}
	return out, nil
}
