//go:build !linux && !windows

package l3

func newBackend() (L3Backend, error) {
	return nil, errBackendUnavailable
}
