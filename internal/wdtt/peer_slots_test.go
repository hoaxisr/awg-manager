package wdtt

import "testing"

// Конфиг, созданный до появления слотов: адрес живёт только в Peer.
func TestNormalizePeers_SeedsSlotFromLegacyPeer(t *testing.T) {
	got := normalizePeers(ClientConfig{ConnMode: ConnModeWG, Peer: "1.1.1.1:56002"})
	if got.PeerWg != "1.1.1.1:56002" {
		t.Errorf("PeerWg = %q, ожидался адрес из Peer", got.PeerWg)
	}
	if got.PeerRaw != "" {
		t.Errorf("PeerRaw = %q, слот соседнего режима трогать нельзя", got.PeerRaw)
	}
}

// Свежий Peer главнее протухшего слота — иначе обновление подписки
// откатывалось бы к прежнему адресу.
func TestNormalizePeers_FreshPeerWinsOverStaleSlot(t *testing.T) {
	got := normalizePeers(ClientConfig{
		ConnMode: ConnModeWG,
		Peer:     "2.2.2.2:56002",
		PeerWg:   "1.1.1.1:56002",
	})
	if got.Peer != "2.2.2.2:56002" || got.PeerWg != "2.2.2.2:56002" {
		t.Fatalf("Peer = %q, PeerWg = %q, ожидался свежий адрес в обоих", got.Peer, got.PeerWg)
	}
}

// Слот соседнего режима переживает работу в текущем режиме.
func TestNormalizePeers_KeepsOtherModeSlot(t *testing.T) {
	got := normalizePeers(ClientConfig{
		ConnMode: ConnModeRaw,
		Peer:     "1.1.1.1:56003",
		PeerWg:   "1.1.1.1:56002",
	})
	if got.PeerRaw != "1.1.1.1:56003" {
		t.Errorf("PeerRaw = %q", got.PeerRaw)
	}
	if got.PeerWg != "1.1.1.1:56002" {
		t.Errorf("PeerWg = %q — слот WG затёрт работой в raw", got.PeerWg)
	}
}

// Пустой Peer восстанавливается из слота активного режима.
func TestNormalizePeers_RestoresPeerFromSlot(t *testing.T) {
	got := normalizePeers(ClientConfig{ConnMode: ConnModeRaw, PeerRaw: "1.1.1.1:56003"})
	if got.Peer != "1.1.1.1:56003" {
		t.Errorf("Peer = %q, ожидалось восстановление из PeerRaw", got.Peer)
	}
}

// Обновление подписки не должно откатывать адрес: ApplyImport кладёт свежий
// peer и в Peer, и в слот активного режима.
func TestApplyImport_RefreshDoesNotRollBackPeer(t *testing.T) {
	cfg := ClientConfig{
		ConnMode: ConnModeWG,
		Peer:     "1.1.1.1:56002",
		PeerWg:   "1.1.1.1:56002",
	}
	got := ApplyImport(cfg, ImportPayload{Peer: "3.3.3.3:56002"})
	if got.Peer != "3.3.3.3:56002" {
		t.Errorf("Peer = %q", got.Peer)
	}
	if got.PeerWg != "3.3.3.3:56002" {
		t.Errorf("PeerWg = %q — слот остался на прежнем сервере, следующее сохранение откатит peer", got.PeerWg)
	}
}
