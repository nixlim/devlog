package sink

import (
	"encoding/json"
	"fmt"
	"net"
	"time"
)

const (
	socketDialTimeout  = 10 * time.Millisecond
	socketWriteTimeout = 30 * time.Millisecond
)

// Socket sends events to a Unix domain socket as NDJSON lines. Each
// Emit call connects, writes, and disconnects — the devlog capture hook
// is a short-lived process with no persistent state to hold a connection.
//
// If the remote (e.g. attest daemon) isn't listening, Emit fails fast
// on the dial (~1ms) and the error is swallowed by the caller.
type Socket struct {
	path string
}

func NewSocket(path string) *Socket {
	return &Socket{path: path}
}

func (s *Socket) Emit(event Event) error {
	data, err := json.Marshal(event)
	if err != nil {
		return fmt.Errorf("sink/socket: encode: %w", err)
	}
	data = append(data, '\n')

	conn, err := net.DialTimeout("unix", s.path, socketDialTimeout)
	if err != nil {
		return fmt.Errorf("sink/socket: dial %s: %w", s.path, err)
	}
	defer conn.Close()

	if err := conn.SetWriteDeadline(time.Now().Add(socketWriteTimeout)); err != nil {
		return fmt.Errorf("sink/socket: set deadline: %w", err)
	}
	if _, err := conn.Write(data); err != nil {
		return fmt.Errorf("sink/socket: write: %w", err)
	}
	return nil
}

func (s *Socket) Close() error {
	return nil
}
