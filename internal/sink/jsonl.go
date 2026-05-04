package sink

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sync"
)

// JSONL writes events as newline-delimited JSON to a file. The simplest
// sink — consumers read by tailing the file. Useful for debugging and
// for consumers that prefer polling over real-time delivery.
type JSONL struct {
	path string
	mu   sync.Mutex
	f    *os.File
}

func NewJSONL(path string) (*JSONL, error) {
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return nil, fmt.Errorf("sink/jsonl: ensure dir for %s: %w", path, err)
	}
	f, err := os.OpenFile(path, os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0o644)
	if err != nil {
		return nil, fmt.Errorf("sink/jsonl: open %s: %w", path, err)
	}
	return &JSONL{path: path, f: f}, nil
}

func (s *JSONL) Emit(event Event) error {
	data, err := json.Marshal(event)
	if err != nil {
		return fmt.Errorf("sink/jsonl: encode: %w", err)
	}
	data = append(data, '\n')

	s.mu.Lock()
	defer s.mu.Unlock()

	if s.f == nil {
		return fmt.Errorf("sink/jsonl: closed")
	}
	if _, err := s.f.Write(data); err != nil {
		return fmt.Errorf("sink/jsonl: write %s: %w", s.path, err)
	}
	return nil
}

func (s *JSONL) Close() error {
	s.mu.Lock()
	defer s.mu.Unlock()

	if s.f == nil {
		return nil
	}
	err := s.f.Close()
	s.f = nil
	return err
}
