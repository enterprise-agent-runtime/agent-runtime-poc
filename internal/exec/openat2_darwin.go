package exec

// probeOpenat2: macOS has no openat2, so OpenCanonical always uses the
// component walk there. A06 §5.2 also allows openat(O_NOFOLLOW_ANY) on
// macOS 11+; the walk gives the same outcomes and is the one path the
// Linux fallback tests exercise, so it is used alone.
func probeOpenat2(int) func(int, string, int) (int, error) { return nil }
