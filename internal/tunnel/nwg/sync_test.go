package nwg

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/hoaxisr/awg-manager/internal/logging"
	"github.com/hoaxisr/awg-manager/internal/ndms/transport"
	"github.com/hoaxisr/awg-manager/internal/storage"
)

// captureServer records every POST body the transport.Client sends and
// replies with a length-matched empty array so PostBatch's response
// decoding succeeds.
type captureServer struct {
	srv    *httptest.Server
	bodies []string
}

func newCaptureServer(t *testing.T) *captureServer {
	t.Helper()
	cs := &captureServer{}
	cs.srv = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		cs.bodies = append(cs.bodies, string(body))
		// Detect batch vs single payload and reply with a matching shape so
		// transport.Client.PostBatch and Post both decode successfully.
		trimmed := strings.TrimLeft(string(body), " \t\r\n")
		if strings.HasPrefix(trimmed, "[") {
			var arr []any
			if err := json.Unmarshal(body, &arr); err == nil {
				out := make([]map[string]any, len(arr))
				for i := range out {
					out[i] = map[string]any{}
				}
				_ = json.NewEncoder(w).Encode(out)
				return
			}
		}
		_, _ = w.Write([]byte(`{}`))
	}))
	t.Cleanup(cs.srv.Close)
	return cs
}

// newSyncTestOperator builds the smallest OperatorNativeWG that SyncPeer
// needs. queries/commands/kmod/hookNotifier stay nil — SyncPeer never
// touches them.
func newSyncTestOperator(t *testing.T, srvURL string) *OperatorNativeWG {
	t.Helper()
	sem := transport.NewSemaphore(2)
	return &OperatorNativeWG{
		transport:   transport.NewWithURL(srvURL, sem),
		appLog:      logging.NewScopedLogger(nil, logging.GroupTunnel, logging.SubOps),
		supportsASC: func() bool { return true }, // ASC — путь по умолчанию в тестах
	}
}

func TestSyncPeer_NoPreviousKey_OnlyAddsPeer(t *testing.T) {
	cs := newCaptureServer(t)
	op := newSyncTestOperator(t, cs.srv.URL)

	stored := &storage.AWGTunnel{
		NWGIndex: 5,
		Peer: storage.AWGPeer{
			PublicKey:  "newkey0000000000000000000000000000000000000=",
			Endpoint:   "1.2.3.4:51820",
			AllowedIPs: []string{"0.0.0.0/0"},
		},
	}

	if err := op.SyncPeer(context.Background(), stored, ""); err != nil {
		t.Fatalf("SyncPeer: %v", err)
	}
	if len(cs.bodies) != 1 {
		t.Fatalf("want 1 POST batch, got %d", len(cs.bodies))
	}
	if strings.Contains(cs.bodies[0], `"no":true`) {
		t.Errorf("batch must not contain peer-no command when previousPublicKey is empty:\n%s", cs.bodies[0])
	}
}

// Доменный endpoint не должен доезжать до конфига NDMS: при неудаче
// резолва NDMS молча не поднимает интерфейс (#702).
func TestSyncPeer_HostnameGoesToNDMSResolved(t *testing.T) {
	cs := newCaptureServer(t)
	op := newSyncTestOperator(t, cs.srv.URL)
	op.resolveFn = func(string) (string, int, error) { return "203.0.113.9", 51820, nil }

	stored := &storage.AWGTunnel{
		NWGIndex: 5,
		Peer: storage.AWGPeer{
			PublicKey: "newkey0000000000000000000000000000000000000=",
			Endpoint:  "vpn.example.com:51820",
		},
	}
	if err := op.SyncPeer(context.Background(), stored, ""); err != nil {
		t.Fatalf("SyncPeer: %v", err)
	}

	joined := strings.Join(cs.bodies, "\n")
	if strings.Contains(joined, "vpn.example.com") {
		t.Fatalf("в конфиг NDMS ушло доменное имя: %s", joined)
	}
	if !strings.Contains(joined, "203.0.113.9:51820") {
		t.Fatalf("резолвнутый адрес в NDMS не ушёл: %s", joined)
	}
}

// Резолв не удался — endpoint не трогаем совсем: прежний литерал в
// конфиге NDMS лучше имени, которое NDMS может не поднять.
func TestSyncPeer_ResolveFailureLeavesEndpointUntouched(t *testing.T) {
	stubResolveGap(t)
	cs := newCaptureServer(t)
	op := newSyncTestOperator(t, cs.srv.URL)
	op.resolveFn = func(string) (string, int, error) { return "", 0, errors.New("i/o timeout") }

	stored := &storage.AWGTunnel{
		NWGIndex: 5,
		Peer: storage.AWGPeer{
			PublicKey: "newkey0000000000000000000000000000000000000=",
			Endpoint:  "vpn.example.com:51820",
		},
	}
	if err := op.SyncPeer(context.Background(), stored, ""); err != nil {
		t.Fatalf("SyncPeer: %v", err)
	}

	joined := strings.Join(cs.bodies, "\n")
	if strings.Contains(joined, "vpn.example.com") {
		t.Fatalf("при неудачном резолве имя всё равно ушло в NDMS: %s", joined)
	}
	if strings.Contains(joined, `"endpoint"`) {
		t.Fatalf("endpoint не должен присутствовать в команде вовсе: %s", joined)
	}
}

// На proxy-прошивке endpoint'ом в NDMS владеет kmod-слот: SyncPeer не
// должен присылать туда ни имя, ни адрес сервера.
func TestSyncPeer_ProxyFirmwareLeavesEndpointToKmod(t *testing.T) {
	cs := newCaptureServer(t)
	op := newSyncTestOperator(t, cs.srv.URL)
	op.supportsASC = func() bool { return false }
	op.resolveFn = func(string) (string, int, error) { return "203.0.113.9", 51820, nil }

	stored := &storage.AWGTunnel{
		NWGIndex: 5,
		Peer: storage.AWGPeer{
			PublicKey: "newkey0000000000000000000000000000000000000=",
			Endpoint:  "vpn.example.com:51820",
		},
	}
	if err := op.SyncPeer(context.Background(), stored, ""); err != nil {
		t.Fatalf("SyncPeer: %v", err)
	}

	joined := strings.Join(cs.bodies, "\n")
	if strings.Contains(joined, "vpn.example.com") || strings.Contains(joined, "203.0.113.9") {
		t.Fatalf("на proxy-пути endpoint в NDMS не наш, а он ушёл: %s", joined)
	}
}

func TestSyncPeer_SameKey_DoesNotRemovePeer(t *testing.T) {
	cs := newCaptureServer(t)
	op := newSyncTestOperator(t, cs.srv.URL)

	sameKey := "samekey0000000000000000000000000000000000000="
	stored := &storage.AWGTunnel{
		NWGIndex: 5,
		Peer: storage.AWGPeer{
			PublicKey:  sameKey,
			Endpoint:   "1.2.3.4:51820",
			AllowedIPs: []string{"0.0.0.0/0"},
		},
	}

	if err := op.SyncPeer(context.Background(), stored, sameKey); err != nil {
		t.Fatalf("SyncPeer: %v", err)
	}
	if len(cs.bodies) != 1 {
		t.Fatalf("want 1 POST batch, got %d", len(cs.bodies))
	}
	if strings.Contains(cs.bodies[0], `"no":true`) {
		t.Errorf("batch must not contain peer-no command when previousPublicKey == current PublicKey:\n%s", cs.bodies[0])
	}
}

// TestSyncPrivateKey_SendsKeyAndSave is the regression test for issue #136
// handshake-fail-after-replace path.
//
// ReplaceConfig replaces stored.Interface (including PrivateKey) wholesale,
// but CmdWireguardPrivateKey is otherwise only emitted in Create. Without
// explicit re-sync, NDMS keeps the old key from the original import — WG
// kernel signs handshake initiators with that old identity, the new
// server's peer entry (which expects the public key derived from the NEW
// PrivateKey) silently rejects them, handshake never completes.
//
// Fix lives in nwg/sync.go:SyncPrivateKey and service/impl.go ReplaceConfig
// (calls SyncPrivateKey before SyncPeer) + applyDiffNWG (calls it on
// PrivateKey diff).
func TestSyncPrivateKey_SendsKeyAndSave(t *testing.T) {
	cs := newCaptureServer(t)
	op := newSyncTestOperator(t, cs.srv.URL)

	stored := &storage.AWGTunnel{
		NWGIndex: 5,
		Interface: storage.AWGInterface{
			PrivateKey: "newPrivateKey000000000000000000000000000000=",
		},
	}

	if err := op.SyncPrivateKey(context.Background(), stored); err != nil {
		t.Fatalf("SyncPrivateKey: %v", err)
	}
	if len(cs.bodies) != 1 {
		t.Fatalf("want 1 POST batch, got %d", len(cs.bodies))
	}
	body := cs.bodies[0]
	if !strings.Contains(body, "private-key") {
		t.Errorf("batch must contain private-key field:\n%s", body)
	}
	if !strings.Contains(body, "newPrivateKey000000000000000000000000000000=") {
		t.Errorf("batch must contain the new key value:\n%s", body)
	}
	if !strings.Contains(body, "save") {
		t.Errorf("batch must contain save command:\n%s", body)
	}
}

// TestSyncPeer_DifferentPreviousKey_RemovesOldPeer is the regression test
// for issue #136 (https://github.com/hoaxisr/awg-manager/issues/136).
//
// When a NativeWG tunnel's config is replaced and the peer's public key
// changes, the SyncPeer batch MUST include a `peer ... no true` command
// for the OLD key. NDMS indexes peers by key — without explicit removal
// the interface ends up with both old and new peers and NDMS reports
// `subnet overlaps with the other peer`.
//
// Fix lives in nwg/sync.go:159-161 and service/impl.go:761 (commit
// e6d488ea, 2026-05-07).
func TestSyncPeer_DifferentPreviousKey_RemovesOldPeer(t *testing.T) {
	cs := newCaptureServer(t)
	op := newSyncTestOperator(t, cs.srv.URL)

	oldKey := "oldkey0000000000000000000000000000000000000Y="
	newKey := "newkey0000000000000000000000000000000000000c="
	stored := &storage.AWGTunnel{
		NWGIndex: 5,
		Peer: storage.AWGPeer{
			PublicKey:  newKey,
			Endpoint:   "4.4.4.4:4444",
			AllowedIPs: []string{"0.0.0.0/0", "::/0"},
		},
	}

	if err := op.SyncPeer(context.Background(), stored, oldKey); err != nil {
		t.Fatalf("SyncPeer: %v", err)
	}
	if len(cs.bodies) != 1 {
		t.Fatalf("want 1 POST batch, got %d (bodies=%v)", len(cs.bodies), cs.bodies)
	}

	body := cs.bodies[0]

	// Must contain the OLD key — referenced by the peer-no command.
	if !strings.Contains(body, oldKey) {
		t.Errorf("batch must reference old key %q:\n%s", oldKey, body)
	}
	if !strings.Contains(body, `"no":true`) {
		t.Errorf("batch must contain peer-no command for old key:\n%s", body)
	}
	// And must add the new peer.
	if !strings.Contains(body, newKey) {
		t.Errorf("batch must reference new key %q:\n%s", newKey, body)
	}

	// peer-no MUST appear before peer-add in the batch JSON. NDMS may
	// not respect strict ordering, but the client must always emit them
	// in this order — if a future refactor swaps them, this test fires.
	noIdx := strings.Index(body, `"no":true`)
	addIdx := strings.Index(body, newKey)
	if noIdx < 0 || addIdx < 0 || noIdx > addIdx {
		t.Errorf("expected peer-no BEFORE peer-add in batch (noIdx=%d, addIdx=%d):\n%s",
			noIdx, addIdx, body)
	}
}
