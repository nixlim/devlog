package host

// ModelDiscoverer is an optional interface a host may implement to
// auto-detect available models. Install uses this to populate
// summarizer_model and companion_model in config.json when the user
// doesn't provide explicit --summarizer-model / --companion-model flags.
type ModelDiscoverer interface {
	DiscoverModels() (summarizer, companion string, err error)
}
