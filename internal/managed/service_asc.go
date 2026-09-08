package managed

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"

	"github.com/hoaxisr/awg-manager/internal/sys/osdetect"
)

// GetASCParams returns ASC parameters for the managed server's interface.
// Все параметры приходят из NDMS: сигнатуры I1-I5 у сервера нет — она
// принадлежит пиру (CONTEXT.md «Владелец сигнатуры»).
func (s *Service) GetASCParams(ctx context.Context, id string) (json.RawMessage, error) {
	server, ok := s.settings.GetManagedServerByID(id)
	if !ok {
		return nil, fmt.Errorf("managed server not found: %s", id)
	}

	return s.queries.WGServers.GetASCParams(ctx, server.InterfaceName, osdetect.AtLeast(5, 1))
}

// SetASCParams sets ASC parameters on the managed server's interface.
// I1-I5 из тела отбрасываются перед отправкой в NDMS и больше нигде не
// сохраняются: сигнатура принадлежит пиру.
func (s *Service) SetASCParams(ctx context.Context, id string, params json.RawMessage) error {
	server, ok := s.settings.GetManagedServerByID(id)
	if !ok {
		return fmt.Errorf("managed server not found: %s", id)
	}
	if err := validateASCParamsRequired(params); err != nil {
		return err
	}

	if err := s.applyASCParams(ctx, server.InterfaceName, params); err != nil {
		s.appLog.Warn("set-asc", server.InterfaceName, "Failed to set ASC params: "+err.Error())
		return err
	}

	s.appLog.Full("set-asc", server.InterfaceName, "ASC params updated")
	return nil
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
