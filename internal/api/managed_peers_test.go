package api

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/hoaxisr/awg-manager/internal/managed"
	"github.com/hoaxisr/awg-manager/internal/signature"
	"github.com/hoaxisr/awg-manager/internal/storage"
)

// stubPeerSvc подменяет только пировые методы: остальной ManagedServerService
// в этих тестах не вызывается, поэтому встроенный nil-интерфейс безопасен.
type stubPeerSvc struct {
	managed.ManagedServerService
	updateErr error
	addErr    error
}

func (s *stubPeerSvc) UpdatePeer(context.Context, string, string, managed.UpdatePeerRequest) error {
	return s.updateErr
}

func (s *stubPeerSvc) AddPeer(context.Context, string, managed.AddPeerRequest) (*storage.ManagedPeer, error) {
	return nil, s.addErr
}

// Q32: фронт различает «плохой профиль», «сигнатура не влезла» и всё
// остальное по коду, а не по тексту ошибки.
func TestUpdatePeerHandler_SignatureErrorCodes(t *testing.T) {
	cases := []struct {
		name string
		err  error
		want string
	}{
		{"unknown profile", managed.ErrUnknownSignatureProfile, "INVALID_SIGNATURE_PROFILE"},
		{"too large", managed.ErrSignatureTooLarge, "SIGNATURE_TOO_LARGE"},
		{"anything else", errors.New("peer not found"), "UPDATE_PEER_FAILED"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			h := &ManagedServerHandler{svc: &stubPeerSvc{updateErr: c.err}}
			rec := httptest.NewRecorder()
			h.UpdatePeer(rec, httptest.NewRequest(http.MethodPut, "/managed-servers/Wireguard0/peers/PUB",
				strings.NewReader(`{"description":"d"}`)), "Wireguard0", "PUB")
			if !strings.Contains(rec.Body.String(), c.want) {
				t.Fatalf("нет кода %s в теле: %s", c.want, rec.Body.String())
			}
		})
	}
}

// Q32: импорт .conf с негабаритной сигнатурой отличается от прочих отказов
// импорта отдельным кодом — иначе клиенту нечего показать на поле I1-I5.
func TestImportConf_OversizedSignatureIsSignatureTooLarge(t *testing.T) {
	store := storage.NewAWGTunnelStore(t.TempDir())
	svc := &importStubSvc{importErr: fmt.Errorf("I1–I5: %w", signature.ErrPacketsTooLarge)}
	h := NewImportHandler(svc, store, &appLogSpy{})

	rec := httptest.NewRecorder()
	h.ImportConf(rec, httptest.NewRequest(http.MethodPost, "/api/tunnels/import",
		strings.NewReader(`{"content":"[Interface]","name":"imported"}`)))

	if !strings.Contains(rec.Body.String(), "SIGNATURE_TOO_LARGE") {
		t.Fatalf("нет кода SIGNATURE_TOO_LARGE в теле: %s", rec.Body.String())
	}
}

// AddPeer фейлится закрыто, когда генератор сигнатуры не отработал — код
// должен отличаться от общего ADD_PEER_FAILED.
func TestAddPeerHandler_SignatureGenerateCode(t *testing.T) {
	h := &ManagedServerHandler{svc: &stubPeerSvc{addErr: managed.ErrSignatureGenerate}}
	rec := httptest.NewRecorder()
	h.AddPeer(rec, httptest.NewRequest(http.MethodPost, "/managed-servers/Wireguard0/peers",
		strings.NewReader(`{"description":"d","tunnelIP":"10.0.0.2/32"}`)), "Wireguard0")
	if !strings.Contains(rec.Body.String(), "SIGNATURE_GENERATE_FAILED") {
		t.Fatalf("нет кода SIGNATURE_GENERATE_FAILED в теле: %s", rec.Body.String())
	}
}
