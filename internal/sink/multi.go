package sink

import (
	"fmt"
	"strings"
)

// Multi fans out events to N sinks. Emission continues through all sinks
// even when some fail — errors are collected and returned as a combined
// error. This ensures one broken sink doesn't suppress delivery to others.
type Multi struct {
	sinks []Sink
}

func NewMulti(sinks ...Sink) *Multi {
	return &Multi{sinks: sinks}
}

func (m *Multi) Emit(event Event) error {
	var errs []string
	for _, s := range m.sinks {
		if err := s.Emit(event); err != nil {
			errs = append(errs, err.Error())
		}
	}
	if len(errs) > 0 {
		return fmt.Errorf("sink/multi: %s", strings.Join(errs, "; "))
	}
	return nil
}

func (m *Multi) Close() error {
	var errs []string
	for _, s := range m.sinks {
		if err := s.Close(); err != nil {
			errs = append(errs, err.Error())
		}
	}
	if len(errs) > 0 {
		return fmt.Errorf("sink/multi: close: %s", strings.Join(errs, "; "))
	}
	return nil
}
