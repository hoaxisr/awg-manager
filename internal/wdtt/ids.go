package wdtt

import (
	"crypto/rand"
	"encoding/hex"
)

const (
	DefaultInstanceID = "default"
	ConfigVersion     = 1
)

func newInstanceID() string {
	var b [12]byte
	if _, err := rand.Read(b[:]); err != nil {
		panic(err)
	}
	return hex.EncodeToString(b[:])
}
