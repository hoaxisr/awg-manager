//go:build !windows

package main

import (
	"os"
	"os/signal"
	"syscall"
)

func notifySignals(ch chan os.Signal) {
	// SIGHUP is what a closed terminal window sends the foreground process
	// (not SIGINT/SIGTERM) - without catching it, closing the window instead
	// of Ctrl+C skips the bypass-route cleanup in SocketWatcher.Stop() entirely.
	signal.Notify(ch, syscall.SIGINT, syscall.SIGTERM, syscall.SIGHUP)
}
