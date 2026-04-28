package managed

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"

	"github.com/hoaxisr/awg-manager/internal/ndms"
	"github.com/hoaxisr/awg-manager/internal/storage"
	"github.com/hoaxisr/awg-manager/internal/sys/osdetect"
)

// GetASCParams returns ASC parameters for the managed server's interface.
// Numeric params (Jc..H4, S3, S4) come from NDMS; I1-I5 come from local storage.
func (s *Service) GetASCParams(ctx context.Context, id string) (json.RawMessage, error) {
	server, ok := s.settings.GetManagedServerByID(id)
	if !ok {
		return nil, fmt.Errorf("managed server not found: %s", id)
	}

	raw, err := s.queries.WGServers.GetASCParams(ctx, server.InterfaceName, osdetect.AtLeast(5, 1))
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
func (s *Service) SetASCParams(ctx context.Context, id string, params json.RawMessage) error {
	server, ok := s.settings.GetManagedServerByID(id)
	if !ok {
		return fmt.Errorf("managed server not found: %s", id)
	}

	// Extract I1-I5 from params and persist locally first so the strip-and-send
	// path below can fail without leaving stale local state.
	var ext ndms.ASCParamsExtended
	hasI := false
	if err := json.Unmarshal(params, &ext); err == nil {
		hasI = true
	}

	if hasI {
		if err := s.settings.UpdateManagedServer(id, func(sv *storage.ManagedServer) error {
			sv.I1 = ext.I1
			sv.I2 = ext.I2
			sv.I3 = ext.I3
			sv.I4 = ext.I4
			sv.I5 = ext.I5
			return nil
		}); err != nil {
			return fmt.Errorf("save I1-I5: %w", err)
		}
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

	if err := s.commands.Wireguard.SetASCParams(ctx, server.InterfaceName, stripped); err != nil {
		s.appLog.Warn("set-asc", server.InterfaceName, "Failed to set ASC params: "+err.Error())
		return err
	}

	s.appLog.Full("set-asc", server.InterfaceName, "ASC params updated")
	return nil
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
