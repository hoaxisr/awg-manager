package freeturn

import (
	"crypto/rand"
	"encoding/hex"
)

const (
	// DefaultInstanceID is the singleton client/server id in v1 configs.
	DefaultInstanceID = "default"
	// ConfigVersion is the current freeturn.json schema version.
	ConfigVersion = 2
)

func newInstanceID() string {
	var b [12]byte
	if _, err := rand.Read(b[:]); err != nil {
		panic(err)
	}
	return hex.EncodeToString(b[:])
}
