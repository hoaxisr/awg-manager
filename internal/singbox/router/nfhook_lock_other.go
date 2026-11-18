//go:build !linux

package router

func lockNetfilterHookFile(_ string) func() {
	return func() {}
}
