package host

import "sync"

var (
	mu       sync.RWMutex
	registry = map[string]func() Host{}
)

// Register associates a host name with a constructor. Called from the
// init() of each concrete host package so that importing internal/host
// via a blank import wires up the available backends.
func Register(name string, ctor func() Host) {
	mu.Lock()
	defer mu.Unlock()
	registry[name] = ctor
}

// Lookup returns a fresh host for the given name. The bool is false when
// no host has registered under that name.
func Lookup(name string) (Host, bool) {
	mu.RLock()
	defer mu.RUnlock()
	ctor, ok := registry[name]
	if !ok {
		return nil, false
	}
	return ctor(), true
}

// RegisteredNames returns the names of every currently registered host,
// in unspecified order. Used by `devlog install` / config helpers to show
// available options.
func RegisteredNames() []string {
	mu.RLock()
	defer mu.RUnlock()
	names := make([]string, 0, len(registry))
	for n := range registry {
		names = append(names, n)
	}
	return names
}
