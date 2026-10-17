package platform

// CloseOnExecInherited marks every descriptor >= 3 of this process
// close-on-exec, so nothing this process inherited reaches a child it
// starts (M2-13b, CWE-403; design A06 §11.1 "Go marks every other daemon
// fd close-on-exec" holds only for descriptors Go opened itself).
//
// It never closes anything; it only sets FD_CLOEXEC. That is safe for the
// daemon: every descriptor Go opens is already close-on-exec, so for them
// this changes nothing, and exec.Cmd.ExtraFiles and Stdin/Stdout/Stderr are
// dup2'ed into the child by Go regardless of the flag. A descriptor
// inherited from whatever started the process (a shell, a terminal, a
// service manager, a CI runner) stays usable here and is simply not passed
// on. Descriptors 0-2 are untouched.
//
// Linux uses close_range(3, ~0, CLOSE_RANGE_CLOEXEC) (Linux >= 5.11) and
// falls back to flagging each entry of /proc/self/fd; macOS flags each
// entry of /dev/fd; on Windows, handles are not inherited unless asked for
// (Go starts children with an explicit handle list), so it does nothing.
func CloseOnExecInherited() error { return closeOnExecInherited() }
