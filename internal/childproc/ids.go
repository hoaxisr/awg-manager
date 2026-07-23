// Package childproc holds the process-management primitives shared by the
// freeturn and wdtt daemons: instance-id generation, a stderr ring buffer,
// platform-specific signal/liveness helpers, and a download+verify+install
// helper. Domain-specific lifecycle (startup grace, stale-pidfile handling,
// registry role naming) stays in each package.
package childproc

import (
	"crypto/rand"
	"encoding/hex"
)

// NewInstanceID returns a random 24-hex-char instance id.
func NewInstanceID() string {
	var b [12]byte
	if _, err := rand.Read(b[:]); err != nil {
		panic(err)
	}
	return hex.EncodeToString(b[:])
}
