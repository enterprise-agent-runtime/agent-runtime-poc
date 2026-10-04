package platform

import "os"

// sandboxed: host-trusted self-spawn of wardend, never agent-requested
func spawn() { _, _ = os.StartProcess("wardend", nil, nil) }
