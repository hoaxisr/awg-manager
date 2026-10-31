package dnsrewrite

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
)

var (
	ErrConflict = errors.New("DNS Rewrite changed; read the list again")
	ErrManaged  = errors.New("перезапись управляется пресетом")
)

// Revision identifies the complete ordered list, including addresses and
// owners. Checking just an entry cannot detect an index shift onto an identical
// duplicate. The token is computed on read and is never persisted.
func Revision(items []DNSRewrite) string {
	data, _ := json.Marshal(items) // Entries contain only strings and string slices.
	sum := sha256.Sum256(data)
	return hex.EncodeToString(sum[:])
}
