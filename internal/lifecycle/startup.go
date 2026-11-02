package lifecycle

// EnsureStartup applies the default once per launch without registry writes
// when already registered. Explicit opt-out skips even the registration probe.
// This is a startup action, not part of the event loop or periodic fallback.
func EnsureStartup(enabled bool, current func() (bool, error), register func() error) error {
	if !enabled {
		return nil
	}
	active, err := current()
	if err != nil || active {
		return err
	}
	return register()
}
