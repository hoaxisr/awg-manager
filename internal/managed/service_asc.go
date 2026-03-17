package managed

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"

	"github.com/hoaxisr/awg-manager/internal/tunnel/ndms"
)

// GetASCParams returns ASC parameters for the managed server's interface.
// Numeric params (Jc..H4, S3, S4) come from NDMS; I1-I5 come from local storage.
func (s *Service) GetASCParams(ctx context.Context) (json.RawMessage, error) {
	server := s.settings.GetManagedServer()
	if server == nil {
		return nil, fmt.Errorf("no managed server exists")
	}

	raw, err := s.ndms.GetASCParams(ctx, server.InterfaceName)
	if err != nil {
		return nil, err
	}

	// Merge locally stored I1-I5 into the NDMS response.
	// I1-I5 are client-only params stored locally, not on NDMS.
	var params map[string]json.RawMessage
	if err := json.Unmarshal(raw, &params); err != nil {
		return raw, nil
	}

	needsMerge := false
	if server.I1 != "" {
		params["i1"] = marshalStringRaw(server.I1)
		needsMerge = true
	}
	if server.I2 != "" {
		params["i2"] = marshalStringRaw(server.I2)
		needsMerge = true
	}
	if server.I3 != "" {
		params["i3"] = marshalStringRaw(server.I3)
		needsMerge = true
	}
	if server.I4 != "" {
		params["i4"] = marshalStringRaw(server.I4)
		needsMerge = true
	}
	if server.I5 != "" {
		params["i5"] = marshalStringRaw(server.I5)
		needsMerge = true
	}

	if needsMerge {
		return marshalNoEscape(params)
	}
	return raw, nil
}

// SetASCParams sets ASC parameters on the managed server's interface.
// I1-I5 are saved locally (not sent to NDMS — server doesn't use them).
func (s *Service) SetASCParams(ctx context.Context, params json.RawMessage) error {
	server := s.settings.GetManagedServer()
	if server == nil {
		return fmt.Errorf("no managed server exists")
	}

	// Extract I1-I5 from params and save locally
	var ext ndms.ASCParamsExtended
	if err := json.Unmarshal(params, &ext); err == nil {
		server.I1 = ext.I1
		server.I2 = ext.I2
		server.I3 = ext.I3
		server.I4 = ext.I4
		server.I5 = ext.I5
	}

	if err := s.settings.SaveManagedServer(server); err != nil {
		return fmt.Errorf("save I1-I5: %w", err)
	}

	// Strip I1-I5 before sending to NDMS
	var raw map[string]json.RawMessage
	if err := json.Unmarshal(params, &raw); err != nil {
		return fmt.Errorf("parse ASC params: %w", err)
	}
	delete(raw, "i1")
	delete(raw, "i2")
	delete(raw, "i3")
	delete(raw, "i4")
	delete(raw, "i5")

	stripped, err := marshalNoEscape(raw)
	if err != nil {
		return fmt.Errorf("marshal stripped ASC params: %w", err)
	}

	return s.ndms.SetASCParams(ctx, server.InterfaceName, stripped)
}

// marshalStringRaw marshals a string to JSON without HTML escaping.
// Preserves <> characters used in I1-I5 signature packets.
func marshalStringRaw(s string) json.RawMessage {
	b, _ := marshalNoEscape(s)
	return b
}

// marshalNoEscape marshals v to JSON without HTML escaping (<, >, &).
func marshalNoEscape(v interface{}) (json.RawMessage, error) {
	var buf bytes.Buffer
	enc := json.NewEncoder(&buf)
	enc.SetEscapeHTML(false)
	if err := enc.Encode(v); err != nil {
		return nil, err
	}
	// Encode appends \n, trim it
	b := buf.Bytes()
	if len(b) > 0 && b[len(b)-1] == '\n' {
		b = b[:len(b)-1]
	}
	return b, nil
}
