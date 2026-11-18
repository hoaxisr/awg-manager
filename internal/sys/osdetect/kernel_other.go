//go:build !linux

package osdetect

func KernelRelease() string {
	return ""
}
