package awg3endpoint

import (
	"encoding/json"
	"errors"
)

type Record struct {
	ID       string          `json:"id"`
	Tag      string          `json:"tag"`
	Endpoint json.RawMessage `json:"endpoint"`
}

var (
	ErrNotAwg            = errors.New("не sing-box awg-endpoint (type != \"awg\")")
	ErrMissingKey        = errors.New("отсутствует private_key")
	ErrMissingPeer       = errors.New("нужен peers[0] с public_key и address")
	ErrHeaderProtectionS = errors.New("S1-S4 должны быть ≥ 8 при header_protection_key")
	ErrTag               = errors.New("недопустимый или занятый тег")
)
