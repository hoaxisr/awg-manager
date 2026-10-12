//go:build !linux

package mihomo

// AWG Manager runs on Linux. Keeping a no-op implementation lets host-side
// tests and developer builds compile on other platforms.
func cleanupStaleManagedProcesses(_, _ string) error {
	return nil
}
