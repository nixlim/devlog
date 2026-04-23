package host

// Configurable is an optional interface a host may implement if the cmd
// layer needs to push CLI command overrides (from Config.ClaudeCommand)
// into a freshly-looked-up host without knowing its concrete type.
//
// Kept separate from Host so OpenCode-style hosts that don't shell out
// to a single binary don't have to stub SetCommand.
type Configurable interface {
	SetCommand(cmd string)
}
