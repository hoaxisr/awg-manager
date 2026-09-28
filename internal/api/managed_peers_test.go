package api

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/hoaxisr/awg-manager/internal/managed"
	"github.com/hoaxisr/awg-manager/internal/peersubnet"
	"github.com/hoaxisr/awg-manager/internal/signature"
	"github.com/hoaxisr/awg-manager/internal/storage"
)

// stubPeerSvc подменяет только пировые методы: остальной ManagedServerService
// в этих тестах не вызывается, поэтому встроенный nil-интерфейс безопасен.
type stubPeerSvc struct {
	managed.ManagedServerService
	updateErr  error
	addErr     error
	presets    peersubnet.Presets
	presetsErr error
	toggleErr  error
}

func (s *stubPeerSvc) TogglePeer(context.Context, string, string, bool) error {
	return s.toggleErr
}

func (s *stubPeerSvc) UpdatePeer(context.Context, string, string, managed.UpdatePeerRequest) error {
	return s.updateErr
}

func (s *stubPeerSvc) AddPeer(context.Context, string, managed.AddPeerRequest) (*storage.ManagedPeer, error) {
	return nil, s.addErr
}

func (s *stubPeerSvc) PeerPresets(context.Context, string, string) (peersubnet.Presets, error) {
	return s.presets, s.presetsErr
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
		// Fix round 4: пир снят с роутера мимо панели — понятный код, не «сбой».
		{"peer gone from router", fmt.Errorf("apply remote subnets: %w", peersubnet.ErrPeerNotFound), `"code":"NOT_FOUND"`},
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

// M2: пир снят с роутера мимо панели — вкл/выкл отвечает NOT_FOUND, прочие
// отказы — TOGGLE_FAILED.
func TestTogglePeerHandler_ErrorCodes(t *testing.T) {
	for want, err := range map[string]error{
		`"code":"NOT_FOUND"`:     fmt.Errorf("toggle peer: %w", peersubnet.ErrPeerNotFound),
		`"code":"TOGGLE_FAILED"`: errors.New("rci down"),
	} {
		h := &ManagedServerHandler{svc: &stubPeerSvc{toggleErr: err}}
		rec := httptest.NewRecorder()
		h.TogglePeer(rec, httptest.NewRequest(http.MethodPost, "/managed-servers/Wireguard0/peers/PUB/toggle",
			strings.NewReader(`{"enabled":false}`)), "Wireguard0", "PUB")
		if rec.Code != http.StatusBadRequest || !strings.Contains(rec.Body.String(), want) {
			t.Fatalf("want %s: code=%d body=%s", want, rec.Code, rec.Body.String())
		}
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

// Общий код по сентинелам peersubnet — один и тот же на AddPeer и UpdatePeer.
func TestPeerHandlers_SubnetErrorCodes(t *testing.T) {
	cases := []struct {
		err  error
		want string
	}{
		{fmt.Errorf("x: %w", peersubnet.ErrInvalidClientAllowedIPs), "INVALID_CLIENT_ALLOWED_IPS"},
		{fmt.Errorf("x: %w", peersubnet.ErrInvalidRemoteSubnets), "INVALID_REMOTE_SUBNETS"},
		{fmt.Errorf("x: %w", peersubnet.ErrRemoteSubnetOverlap), "REMOTE_SUBNET_OVERLAP"},
		// W2-P6: сегмент сервера пропал с роутера — ошибка конфигурации сегментов.
		{fmt.Errorf("%w: %q", managed.ErrUnknownLANSegment, "Home"), `"code":"LAN_SEGMENTS_FAILED"`},
	}
	for _, c := range cases {
		h := &ManagedServerHandler{svc: &stubPeerSvc{updateErr: c.err, addErr: c.err}}
		rec := httptest.NewRecorder()
		h.UpdatePeer(rec, httptest.NewRequest(http.MethodPut, "/managed-servers/Wireguard0/peers/PUB", strings.NewReader(`{"description":"d"}`)), "Wireguard0", "PUB")
		if !strings.Contains(rec.Body.String(), c.want) {
			t.Fatalf("update: нет кода %s: %s", c.want, rec.Body.String())
		}
		rec = httptest.NewRecorder()
		h.AddPeer(rec, httptest.NewRequest(http.MethodPost, "/managed-servers/Wireguard0/peers", strings.NewReader(`{"description":"d"}`)), "Wireguard0")
		if !strings.Contains(rec.Body.String(), c.want) {
			t.Fatalf("add: нет кода %s: %s", c.want, rec.Body.String())
		}
	}
}

// GET .../peers/presets?dns= через Subtree: успех, отказ по DNS, чужой метод.
func TestManagedSubtree_PeerPresets(t *testing.T) {
	h := &ManagedServerHandler{svc: &stubPeerSvc{presets: peersubnet.Presets{RouterOnly: "10.10.0.0/24", ExceptRouter: "0.0.0.0/1, ::/0"}}}
	rec := httptest.NewRecorder()
	h.Subtree(rec, httptest.NewRequest(http.MethodGet, "/api/managed-servers/Wireguard1/peers/presets?dns=1.1.1.1", nil))
	if rec.Code != 200 || !strings.Contains(rec.Body.String(), `"routerOnly":"10.10.0.0/24"`) || !strings.Contains(rec.Body.String(), `"exceptRouter":"0.0.0.0/1, ::/0"`) {
		t.Fatalf("code=%d body=%s", rec.Code, rec.Body.String())
	}
	h = &ManagedServerHandler{svc: &stubPeerSvc{presetsErr: fmt.Errorf("x: %w", managed.ErrInvalidPeerDNS)}}
	rec = httptest.NewRecorder()
	h.Subtree(rec, httptest.NewRequest(http.MethodGet, "/api/managed-servers/Wireguard1/peers/presets?dns=zzz", nil))
	if !strings.Contains(rec.Body.String(), "INVALID_PEER_DNS") {
		t.Fatalf("body=%s", rec.Body.String())
	}
	rec = httptest.NewRecorder()
	h.Subtree(rec, httptest.NewRequest(http.MethodPost, "/api/managed-servers/Wireguard1/peers/presets", nil))
	if rec.Code != http.StatusMethodNotAllowed {
		t.Fatalf("POST presets: code=%d", rec.Code)
	}
}

// Карточка отдаёт оба поля пира.
func TestToManagedServerResponse_PeerCarriesSubnets(t *testing.T) {
	sv := &storage.ManagedServer{InterfaceName: "Wireguard1", Peers: []storage.ManagedPeer{
		{PublicKey: "P", ClientAllowedIPs: "10.0.0.0/8", RemoteSubnets: []string{"192.168.77.0/24"}}}}
	raw, _ := json.Marshal(toManagedServerResponse(sv, nil))
	for _, want := range []string{`"clientAllowedIPs":"10.0.0.0/8"`, `"remoteSubnets":["192.168.77.0/24"]`} {
		if !strings.Contains(string(raw), want) {
			t.Fatalf("нет %s: %s", want, raw)
		}
	}
}
