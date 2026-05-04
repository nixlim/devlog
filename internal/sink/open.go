package sink

import "fmt"

// Open builds a Sink from a SinkConfig. Returns an error for unknown
// sink types so misconfiguration surfaces at load time.
func Open(cfg SinkConfig) (Sink, error) {
	switch cfg.Type {
	case "unix_socket":
		return NewSocket(cfg.Path), nil
	case "jsonl":
		return NewJSONL(cfg.Path)
	default:
		return nil, fmt.Errorf("sink: unknown type %q (expected unix_socket or jsonl)", cfg.Type)
	}
}

// OpenAll builds a Multi sink from a slice of SinkConfigs. An empty
// slice returns a no-op sink that discards all events. Any single Open
// failure aborts and closes sinks already opened.
func OpenAll(cfgs []SinkConfig) (Sink, error) {
	if len(cfgs) == 0 {
		return &noop{}, nil
	}

	opened := make([]Sink, 0, len(cfgs))
	for _, cfg := range cfgs {
		s, err := Open(cfg)
		if err != nil {
			for _, prev := range opened {
				_ = prev.Close()
			}
			return nil, err
		}
		opened = append(opened, s)
	}

	if len(opened) == 1 {
		return opened[0], nil
	}
	return NewMulti(opened...), nil
}

type noop struct{}

func (noop) Emit(Event) error { return nil }
func (noop) Close() error     { return nil }
