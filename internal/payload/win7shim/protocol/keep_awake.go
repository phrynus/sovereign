package protocol

// Bounds for the keep_awake task, shared by the issuing handler, the agent's
// parser and the generated command reference. The wire command is a decimal
// number of seconds: 0 releases an active request, empty uses the default.
const (
	KeepAwakeDefaultSeconds = 300
	KeepAwakeMaxSeconds     = 86400
)
