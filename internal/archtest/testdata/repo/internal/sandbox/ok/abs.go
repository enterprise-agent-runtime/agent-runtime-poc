package ok

// sandboxed: seeded allowed case; launches the sandbox backend binary
import "golang.org/x/sys/execabs"

var _ = execabs.Command
