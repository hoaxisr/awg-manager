package api

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/hoaxisr/awg-manager/internal/events"
	"github.com/hoaxisr/awg-manager/internal/managed"
	"github.com/hoaxisr/awg-manager/internal/storage"
)

func TestExport_MethodGuard(t *testing.T) {
	h := &ManagedServerBackupHandler{}
	req := httptest.NewRequest(http.MethodPost, "/api/managed/export", nil)
	w := httptest.NewRecorder()
	h.Export(w, req)
	if w.Code != http.StatusMethodNotAllowed {
		t.Errorf("status: got %d, want 405", w.Code)
	}
}

func TestImport_MethodGuard(t *testing.T) {
	h := &ManagedServerBackupHandler{}
	req := httptest.NewRequest(http.MethodGet, "/api/managed/import", nil)
	w := httptest.NewRecorder()
	h.Import(w, req)
	if w.Code != http.StatusMethodNotAllowed {
		t.Errorf("status: got %d, want 405", w.Code)
	}
}

func TestDrift_MethodGuard(t *testing.T) {
	h := &ManagedServerBackupHandler{}
	req := httptest.NewRequest(http.MethodPost, "/api/managed/drift", nil)
	w := httptest.NewRecorder()
	h.Drift(w, req)
	if w.Code != http.StatusMethodNotAllowed {
		t.Errorf("status: got %d, want 405", w.Code)
	}
}

func TestRestoreDrift_MethodGuard(t *testing.T) {
	h := &ManagedServerBackupHandler{}
	req := httptest.NewRequest(http.MethodGet, "/api/managed/restore-drift", nil)
	w := httptest.NewRecorder()
	h.RestoreDrift(w, req)
	if w.Code != http.StatusMethodNotAllowed {
		t.Errorf("status: got %d, want 405", w.Code)
	}
}

func TestImport_RequiresTypeAndVersion(t *testing.T) {
	h := &ManagedServerBackupHandler{svc: &managed.Service{}, bus: events.NewBus()}
	req := httptest.NewRequest(http.MethodPost, "/api/managed/import", bytes.NewBufferString(`{"managedServers":[],"options":{"allowRenumber":false}}`))
	w := httptest.NewRecorder()
	h.Import(w, req)
	if w.Code != http.StatusBadRequest {
		t.Fatalf("status: got %d, want 400", w.Code)
	}
}

func TestBackupDTO_RoundtripPreservesPeerSignature(t *testing.T) {
	src := storage.ManagedServer{
		InterfaceName: "Wireguard7",
		Address:       "10.7.0.1",
		Mask:          "255.255.255.0",
		ListenPort:    51827,
		PrivateKey:    "PRIV7=",
		Policy:        "none",
		Peers: []storage.ManagedPeer{{
			PublicKey: "P", TunnelIP: "10.7.0.2/32",
			I1: "I1", I2: "I2", I3: "I3", I4: "I4", I5: "I5", SignatureProfile: "stun",
		}},
		ASC: []byte(`{"jc":3}`),
	}
	dto := managedServerToBackupDTO(src)
	if dto.LegacyI1 != "" {
		t.Fatalf("new backups must not carry a server-level signature: %+v", dto)
	}
	got := backupDTOToManagedServer(dto)
	p, want := got.Peers[0], src.Peers[0]
	if p.I1 != want.I1 || p.I2 != want.I2 || p.I3 != want.I3 || p.I4 != want.I4 || p.I5 != want.I5 || p.SignatureProfile != want.SignatureProfile {
		t.Fatalf("peer signature mismatch after roundtrip: got=%+v", p)
	}
	if string(got.ASC) != string(src.ASC) {
		t.Fatalf("ASC mismatch after roundtrip: got=%s want=%s", string(got.ASC), string(src.ASC))
	}
}

// Бэкап до схемы 36 нёс одну сигнатуру на сервер — при импорте она
// достаётся пирам без своей, у сервера не остаётся.
func TestBackupDTO_OldServerSignatureGoesToPeers(t *testing.T) {
	dto := ManagedServerBackupDTO{
		InterfaceName: "Wireguard7",
		Address:       "10.7.0.1",
		Mask:          "255.255.255.0",
		ListenPort:    51827,
		Policy:        "none",
		LegacyI1:      "<b 0x0a>",
		LegacyI3:      "<t>",
		Peers: []ManagedPeerDTO{
			{PublicKey: "P1", TunnelIP: "10.7.0.2/32"},
			{PublicKey: "P2", TunnelIP: "10.7.0.3/32", I1: "<b 0xff>", SignatureProfile: "sip"},
		},
	}
	got := backupDTOToManagedServer(dto)
	if got.LegacyI1 != "" || got.LegacyI3 != "" {
		t.Fatalf("server signature must be cleared: %+v", got)
	}
	if got.Peers[0].I1 != "<b 0x0a>" || got.Peers[0].I3 != "<t>" || got.Peers[0].SignatureProfile != "" {
		t.Fatalf("peer without own signature must inherit: %+v", got.Peers[0])
	}
	if got.Peers[1].I1 != "<b 0xff>" || got.Peers[1].I3 != "" || got.Peers[1].SignatureProfile != "sip" {
		t.Fatalf("peer with own signature must keep it: %+v", got.Peers[1])
	}
}

func TestIsEmptyASC(t *testing.T) {
	cases := []struct {
		name string
		in   json.RawMessage
		want bool
	}{
		{name: "nil", in: nil, want: true},
		{name: "empty", in: []byte(""), want: true},
		{name: "spaces", in: []byte("   "), want: true},
		{name: "null", in: []byte("null"), want: true},
		{name: "empty object", in: []byte(`{}`), want: true},
		{name: "zero defaults", in: []byte(`{"jc":0,"jmin":0,"jmax":0,"s1":0,"s2":0,"h1":"","h2":"","h3":"","h4":""}`), want: false},
		{name: "partial invalid", in: []byte(`{"jc":0,"jmin":0,"jmax":0,"s1":0,"s2":0,"h1":"100","h2":"","h3":"","h4":""}`), want: true},
		{name: "invalid jmax<=jmin", in: []byte(`{"jc":3,"jmin":77,"jmax":77,"s1":18,"s2":29,"h1":"103994526","h2":"1201929360","h3":"2403636727","h4":"3602647725"}`), want: true},
		{name: "valid ASC", in: []byte(`{"jc":3,"jmin":77,"jmax":266,"s1":18,"s2":29,"h1":"103994526","h2":"1201929360","h3":"2403636727","h4":"3602647725"}`), want: false},
	}
	for _, tc := range cases {
		if got := isEmptyASC(tc.in); got != tc.want {
			t.Fatalf("%s: isEmptyASC(%q)=%v want %v", tc.name, string(tc.in), got, tc.want)
		}
	}
}
