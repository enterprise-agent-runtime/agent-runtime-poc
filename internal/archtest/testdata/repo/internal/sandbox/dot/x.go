package dot

// sandboxed: seeded case; the justification is present but the dot-import hides every call
import . "os/exec"

var _ = Command
