package managed

import (
	"context"
	"encoding/json"
	"errors"
	"slices"
	"strings"
	"testing"

	"github.com/hoaxisr/awg-manager/internal/ndms/query"
	"github.com/hoaxisr/awg-manager/internal/storage"
)

// W2-P6 (#713): ACL LAN-сегментов managed-сервера пропускает трафик из сетей
// за клиентом в выбранные сегменты. Сегменты харнесса: Bridge0 192.168.1.0/24,
// Bridge1 10.1.30.0/24.

const (
	acl1      = "access-list AWGM_Wireguard1 permit ip "
	acl1Off   = "no access-list AWGM_Wireguard1 permit ip "
	net77     = "192.168.77.0 255.255.255.0 "
	net78     = "192.168.78.0 255.255.255.0 "
	segBr0    = "192.168.1.0 255.255.255.0"
	segBr1    = "10.1.30.0 255.255.255.0"
	aclRebind = "no interface Wireguard1 ip access-group AWGM_Wireguard1 in"
)

func setLANSegments(t *testing.T, store *storage.SettingsStore, iface string, segs ...string) {
	t.Helper()
	if err := store.UpdateManagedServer(iface, func(sv *storage.ManagedServer) error {
		sv.LANSegments = segs
		return nil
	}); err != nil {
		t.Fatal(err)
	}
}

// aclParses — parse-строки ACL в порядке отправки.
func aclParses(p *recordingPoster) []string {
	var out []string
	for _, s := range parseStrings(p) {
		if strings.Contains(s, "access-list") || strings.Contains(s, "access-group") {
			out = append(out, s)
		}
	}
	return out
}

func failParse(sub string) func(map[string]interface{}) error {
	return func(m map[string]interface{}) error {
		if s, ok := m["parse"].(string); ok && strings.Contains(s, sub) {
			return errors.New("acl refused")
		}
		return nil
	}
}

func TestResolveLANSegmentsPlan_IncludesPeerSubnets(t *testing.T) {
	bridges := []query.LANBridge{
		{Name: "Bridge0", Address: "192.168.1.1", Mask: "255.255.255.0"},
		{Name: "Bridge1", Address: "10.1.30.1", Mask: "255.255.255.0"},
	}
	plan, err := resolveLANSegmentsPlan("10.66.66.1", "255.255.255.0", []string{"192.168.77.0/24", "172.16.0.0/16"}, []string{"Bridge0", "Bridge1"}, bridges)
	if err != nil {
		t.Fatal(err)
	}
	var got []string
	for _, r := range plan {
		got = append(got, strings.Join([]string{r.srcSub, r.srcMask, r.dstSub, r.dstMask}, " "))
	}
	want := []string{
		"10.66.66.0 255.255.255.0 192.168.1.0 255.255.255.0",
		"10.66.66.0 255.255.255.0 10.1.30.0 255.255.255.0",
		"192.168.77.0 255.255.255.0 192.168.1.0 255.255.255.0",
		"192.168.77.0 255.255.255.0 10.1.30.0 255.255.255.0",
		"172.16.0.0 255.255.0.0 192.168.1.0 255.255.255.0",
		"172.16.0.0 255.255.0.0 10.1.30.0 255.255.255.0",
	}
	if !slices.Equal(got, want) {
		t.Fatalf("plan:\n%s\nwant:\n%s", strings.Join(got, "\n"), strings.Join(want, "\n"))
	}
}

// Добавление сети: permit на каждый сегмент — после маршрута, точечно, без
// пересборки списка (unbind→bind переставил бы наш список за чужой permit-all).
func TestAddPeer_LANSegments_PermitsEachSegment(t *testing.T) {
	svc, store, poster, fg := newPeerSubnetTestService(t, `[]`)
	newSimRouter(t, fg, poster, `[]`)
	setLANSegments(t, store, "Wireguard1", "Bridge0", "Bridge1")
	if _, err := svc.AddPeer(context.Background(), "Wireguard1", AddPeerRequest{Description: "branch", TunnelIP: "10.66.66.2/32", RemoteSubnets: []string{"192.168.77.0/24"}}); err != nil {
		t.Fatal(err)
	}
	if got, want := aclParses(poster), []string{acl1 + net77 + segBr0, acl1 + net77 + segBr1}; !slices.Equal(got, want) {
		t.Fatalf("acl = %q, want %q", got, want)
	}
	posts := postsJSON(poster)
	if iRoute, iACL := indexOf(posts, route77), indexOf(posts, "access-list"); iRoute < 0 || iACL < iRoute {
		t.Fatalf("ACL обязан идти после маршрута:\n%s", strings.Join(posts, "\n"))
	}
}

func TestUpdatePeer_LANSegments_AddsAndRemovesRules(t *testing.T) {
	svc, store, poster, fg := newPeerSubnetTestService(t, rcOurs77)
	sim := newSimRouter(t, fg, poster, rcOurs77)
	seedSimPeer(t, store, sim, "192.168.77.0/24")
	setLANSegments(t, store, "Wireguard1", "Bridge0", "Bridge1")
	if err := svc.UpdatePeer(context.Background(), "Wireguard1", "PEER1", UpdatePeerRequest{Description: "branch", TunnelIP: "10.66.66.2/32", RemoteSubnets: &[]string{"192.168.78.0/24"}}); err != nil {
		t.Fatal(err)
	}
	want := []string{acl1 + net78 + segBr0, acl1 + net78 + segBr1, acl1Off + net77 + segBr0, acl1Off + net77 + segBr1}
	if got := aclParses(poster); !slices.Equal(got, want) {
		t.Fatalf("acl = %q, want %q", got, want)
	}
	// Неизменный список — ни одного ACL-вызова.
	resetPosts(poster)
	if err := svc.UpdatePeer(context.Background(), "Wireguard1", "PEER1", UpdatePeerRequest{Description: "branch", TunnelIP: "10.66.66.2/32", RemoteSubnets: &[]string{"192.168.78.0/24"}}); err != nil {
		t.Fatal(err)
	}
	if got := aclParses(poster); len(got) != 0 {
		t.Fatalf("acl при неизменных сетях: %q", got)
	}
}

func TestPeerSubnets_NoLANSegments_NoACL(t *testing.T) {
	svc, store, poster, fg := newPeerSubnetTestService(t, rcOurs77)
	sim := newSimRouter(t, fg, poster, rcOurs77)
	seedSimPeer(t, store, sim, "192.168.77.0/24")
	ctx := context.Background()
	if err := svc.UpdatePeer(ctx, "Wireguard1", "PEER1", UpdatePeerRequest{Description: "branch", TunnelIP: "10.66.66.2/32", RemoteSubnets: &[]string{"192.168.78.0/24"}}); err != nil {
		t.Fatal(err)
	}
	if _, err := svc.AddPeer(ctx, "Wireguard1", AddPeerRequest{Description: "x", TunnelIP: "10.66.66.3/32", RemoteSubnets: []string{"192.168.79.0/24"}}); err != nil {
		t.Fatal(err)
	}
	if err := svc.DeletePeer(ctx, "Wireguard1", "PEER1"); err != nil {
		t.Fatal(err)
	}
	if got := aclParses(poster); len(got) != 0 {
		t.Fatalf("acl без LAN-сегментов: %q", got)
	}
}

// Отказ permit → снято добавленное этим вызовом, сверка возвращена к записи,
// хранилище не тронуто.
func TestUpdatePeer_LANACLFailure_RollsBack(t *testing.T) {
	svc, store, poster, fg := newPeerSubnetTestService(t, rcOurs77)
	sim := newSimRouter(t, fg, poster, rcOurs77)
	seedSimPeer(t, store, sim, "192.168.77.0/24")
	setLANSegments(t, store, "Wireguard1", "Bridge0", "Bridge1")
	poster.failOn = failParse(acl1 + net78 + segBr1)
	err := svc.UpdatePeer(context.Background(), "Wireguard1", "PEER1", UpdatePeerRequest{Description: "branch", TunnelIP: "10.66.66.2/32", RemoteSubnets: &[]string{"192.168.77.0/24", "192.168.78.0/24"}})
	if err == nil || !strings.Contains(err.Error(), "acl refused") {
		t.Fatalf("err = %v", err)
	}
	want := []string{acl1 + net78 + segBr0, acl1 + net78 + segBr1, acl1Off + net78 + segBr0}
	if got := aclParses(poster); !slices.Equal(got, want) {
		t.Fatalf("acl = %q, want %q", got, want)
	}
	allow, routes := sim.state("Wireguard1", "PEER1", "awgm-peer:PEER1")
	if !slices.Equal(allow, []string{"10.66.66.2/255.255.255.255", "192.168.77.0/255.255.255.0"}) || !slices.Equal(routes, []string{"192.168.77.0"}) {
		t.Fatalf("сверка не возвращена: allow=%v routes=%v", allow, routes)
	}
	if sv, _ := store.GetManagedServerByID("Wireguard1"); !slices.Equal(sv.Peers[0].RemoteSubnets, []string{"192.168.77.0/24"}) {
		t.Fatalf("store = %v", sv.Peers[0].RemoteSubnets)
	}
}

// Отказ снятия правила → уже снятые возвращаются, добавленные снимаются.
func TestUpdatePeer_LANACLRemoveFailure_RestoresRemovedRules(t *testing.T) {
	svc, store, poster, fg := newPeerSubnetTestService(t, rcOurs77)
	sim := newSimRouter(t, fg, poster, rcOurs77)
	seedSimPeer(t, store, sim, "192.168.77.0/24")
	setLANSegments(t, store, "Wireguard1", "Bridge0", "Bridge1")
	poster.failOn = failParse(acl1Off + net77 + segBr1)
	err := svc.UpdatePeer(context.Background(), "Wireguard1", "PEER1", UpdatePeerRequest{Description: "branch", TunnelIP: "10.66.66.2/32", RemoteSubnets: &[]string{"192.168.78.0/24"}})
	if err == nil {
		t.Fatal("ждали отказ")
	}
	want := []string{
		acl1 + net78 + segBr0, acl1 + net78 + segBr1, acl1Off + net77 + segBr0, acl1Off + net77 + segBr1,
		acl1Off + net78 + segBr0, acl1Off + net78 + segBr1, acl1 + net77 + segBr0,
	}
	if got := aclParses(poster); !slices.Equal(got, want) {
		t.Fatalf("acl = %q\nwant %q", got, want)
	}
}

func TestAddPeer_LANACLFailure_RollsBack(t *testing.T) {
	svc, store, poster, fg := newPeerSubnetTestService(t, `[]`)
	sim := newSimRouter(t, fg, poster, `[]`)
	setLANSegments(t, store, "Wireguard1", "Bridge0", "Bridge1")
	poster.failOn = failParse(acl1 + net77 + segBr1)
	_, err := svc.AddPeer(context.Background(), "Wireguard1", AddPeerRequest{Description: "branch", TunnelIP: "10.66.66.2/32", RemoteSubnets: []string{"192.168.77.0/24"}})
	if err == nil || !strings.Contains(err.Error(), "acl refused") {
		t.Fatalf("err = %v", err)
	}
	if got := aclParses(poster); !slices.Contains(got, acl1Off+net77+segBr0) {
		t.Fatalf("добавленное правило не снято: %q", got)
	}
	if allow, routes := sim.state("Wireguard1", "pub-1", "awgm-peer:pub-1"); len(allow)+len(routes) != 0 {
		t.Fatalf("сироты: allow=%v routes=%v", allow, routes)
	}
	if sv, _ := store.GetManagedServerByID("Wireguard1"); len(sv.Peers) != 0 {
		t.Fatal("пир записан")
	}
}

// Отказ записи → правила ACL этого вызова откатываются вместе со сверкой.
func TestUpdatePeer_StoreFailure_UndoesACL(t *testing.T) {
	svc, store, poster, fg := newPeerSubnetTestService(t, `[]`)
	sim := newSimRouter(t, fg, poster, `[]`)
	seedSimPeer(t, store, sim)
	setLANSegments(t, store, "Wireguard1", "Bridge0")
	sim.after = func(m map[string]interface{}) {
		if s, _ := m["parse"].(string); s == acl1+net77+segBr0 {
			_ = store.UpdateManagedServer("Wireguard1", func(sv *storage.ManagedServer) error {
				sv.Peers = nil
				return nil
			})
		}
	}
	err := svc.UpdatePeer(context.Background(), "Wireguard1", "PEER1", UpdatePeerRequest{Description: "branch", TunnelIP: "10.66.66.2/32", RemoteSubnets: &[]string{"192.168.77.0/24"}})
	if err == nil || !strings.Contains(err.Error(), "save to storage") {
		t.Fatalf("err = %v", err)
	}
	if got, want := aclParses(poster), []string{acl1 + net77 + segBr0, acl1Off + net77 + segBr0}; !slices.Equal(got, want) {
		t.Fatalf("acl = %q, want %q", got, want)
	}
}

// Удаление пира снимает его правила; отказ снятия удаление не блокирует.
func TestDeletePeer_RemovesACLRules_BestEffort(t *testing.T) {
	for _, fail := range []bool{false, true} {
		svc, store, poster, _ := newPeerSubnetTestService(t, rcOurs77)
		seedPeer(t, store, "192.168.77.0/24")
		setLANSegments(t, store, "Wireguard1", "Bridge0", "Bridge1")
		if fail {
			poster.failOn = failParse(acl1Off + net77 + segBr0)
		}
		if err := svc.DeletePeer(context.Background(), "Wireguard1", "PEER1"); err != nil {
			t.Fatalf("fail=%v: %v", fail, err)
		}
		if got, want := aclParses(poster), []string{acl1Off + net77 + segBr0, acl1Off + net77 + segBr1}; !slices.Equal(got, want) {
			t.Fatalf("fail=%v: acl = %q, want %q", fail, got, want)
		}
		if sv, _ := store.GetManagedServerByID("Wireguard1"); len(sv.Peers) != 0 {
			t.Fatalf("fail=%v: пир остался в записи", fail)
		}
	}
}

// Полная пересборка (SetLANSegments) включает сети всех пиров сервера.
func TestSetLANSegments_IncludesPeerSubnets(t *testing.T) {
	svc, store, poster, _ := newPeerSubnetTestService(t, `[]`)
	if err := store.UpdateManagedServer("Wireguard1", func(sv *storage.ManagedServer) error {
		sv.Peers = []storage.ManagedPeer{
			{PublicKey: "PEER1", TunnelIP: "10.66.66.2/32", RemoteSubnets: []string{"192.168.77.0/24"}},
			{PublicKey: "PEER2", TunnelIP: "10.66.66.3/32"},
			{PublicKey: "PEER3", TunnelIP: "10.66.66.4/32", RemoteSubnets: []string{"192.168.78.0/24"}},
		}
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	if !waitsForLock(t, svc, func() {
		if err := svc.SetLANSegments(context.Background(), "Wireguard1", []string{"Bridge0"}); err != nil {
			t.Error(err)
		}
	}) {
		t.Fatal("SetLANSegments прошёл мимо блокировки сетей пиров")
	}
	want := []string{
		aclRebind, "no access-list AWGM_Wireguard1",
		acl1 + "10.66.66.0 255.255.255.0 " + segBr0, acl1 + net77 + segBr0, acl1 + net78 + segBr0,
		"interface Wireguard1 ip access-group AWGM_Wireguard1 in", "access-list AWGM_Wireguard1 auto-delete",
	}
	if got := aclParses(poster); !slices.Equal(got, want) {
		t.Fatalf("acl = %q\nwant %q", got, want)
	}
}

// Восстановление: план ACL включает сети восстановленных пиров — только
// принятые (занятая сеть снята с записи и в ACL не попадает).
func TestRestoreDrift_LANPlanIncludesRestoredPeerSubnets(t *testing.T) {
	svc, store, poster, fg := newPeerSubnetTestService(t, `[]`)
	newSimRouter(t, fg, poster, `[]`)
	if err := store.AddManagedServer(storage.ManagedServer{InterfaceName: "Wireguard5", Address: "10.77.0.1", Mask: "255.255.255.0", ListenPort: 51825,
		PrivateKey: validPrivateKey(9), Policy: "none", LANSegments: []string{"Bridge0"},
		Peers: []storage.ManagedPeer{{PublicKey: "PEER5", TunnelIP: "10.77.0.2/32", Enabled: true, Description: "site", RemoteSubnets: []string{"172.16.5.0/24", "192.168.90.0/24"}}}}); err != nil {
		t.Fatal(err)
	}
	restoreDrift(t, svc)
	var permits []string
	for _, s := range aclParses(poster) {
		if strings.HasPrefix(s, "access-list AWGM_Wireguard5 permit") {
			permits = append(permits, s)
		}
	}
	want := []string{
		"access-list AWGM_Wireguard5 permit ip 10.77.0.0 255.255.255.0 " + segBr0,
		"access-list AWGM_Wireguard5 permit ip 192.168.90.0 255.255.255.0 " + segBr0,
	}
	if !slices.Equal(permits, want) {
		t.Fatalf("permits = %q\nwant %q", permits, want)
	}
}

// Merge-восстановление в сервер с LAN-сегментами: сети добавленного пира
// получают permit в сегменты.
func TestRestoreMerge_PermitsMergedPeerSubnets(t *testing.T) {
	svc, store, poster, fg := newPeerSubnetTestService(t, `[]`)
	newSimRouter(t, fg, poster, `[]`)
	priv := validPrivateKey(58)
	if err := store.UpdateManagedServer("Wireguard1", func(sv *storage.ManagedServer) error {
		sv.PrivateKey = priv
		sv.LANSegments = []string{"Bridge0"}
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	pub := mustDerivePublicKey(t, priv)
	fg.SetPostInterface("Wireguard1", `{"show":{"interface":{"id":"Wireguard1","type":"Wireguard","address":"10.66.66.1","mask":"255.255.255.0","wireguard":{"public-key":"`+pub+`"}}}}`)
	sv, _ := store.GetManagedServerByID("Wireguard1")
	in := []storage.ManagedPeer{{PublicKey: validPeerKey(59), TunnelIP: "10.66.66.2/32", Enabled: true, RemoteSubnets: []string{"192.168.77.0/24"}}}
	out := svc.Restore(context.Background(), []ManagedServerExport{{InterfaceName: "Wireguard1", Address: sv.Address, Mask: sv.Mask, ListenPort: sv.ListenPort, PrivateKey: priv, Policy: "none", Peers: in}}, RestoreOptions{})
	if len(out) != 1 || out[0].Action != "merged" || out[0].AddedPeers != 1 {
		t.Fatalf("outcomes: %+v", out)
	}
	if got, want := aclParses(poster), []string{acl1 + net77 + segBr0}; !slices.Equal(got, want) {
		t.Fatalf("acl = %q, want %q", got, want)
	}
}

// Правило, стоявшее до вызова (NDMS: «a duplicate was found»), откат не снимает.
func TestUpdatePeer_LANACLFailure_KeepsPreexistingRule(t *testing.T) {
	svc, store, poster, fg := newPeerSubnetTestService(t, `[]`)
	sim := newSimRouter(t, fg, poster, `[]`)
	seedSimPeer(t, store, sim)
	setLANSegments(t, store, "Wireguard1", "Bridge0", "Bridge1")
	poster.respond = func(m map[string]interface{}) json.RawMessage {
		if s, _ := m["parse"].(string); s == acl1+net77+segBr0 {
			return json.RawMessage(`[{"parse":{"prompt":"(config)","status":[{"status":"error","ident":"Network::Acl","message":"a duplicate was found for the rule being set."}]}}]`)
		}
		return nil
	}
	poster.failOn = failParse(acl1 + net77 + segBr1)
	if err := svc.UpdatePeer(context.Background(), "Wireguard1", "PEER1", UpdatePeerRequest{Description: "branch", TunnelIP: "10.66.66.2/32", RemoteSubnets: &[]string{"192.168.77.0/24"}}); err == nil {
		t.Fatal("ждали отказ")
	}
	if got := aclParses(poster); slices.Contains(got, acl1Off+net77+segBr0) {
		t.Fatalf("снято чужое (стоявшее до вызова) правило: %q", got)
	}
}

func TestAddPeer_StoreFailure_UndoesACL(t *testing.T) {
	svc, store, poster, fg := newPeerSubnetTestService(t, `[]`)
	sim := newSimRouter(t, fg, poster, `[]`)
	setLANSegments(t, store, "Wireguard1", "Bridge0")
	sim.after = func(m map[string]interface{}) {
		if s, _ := m["parse"].(string); s == acl1+net77+segBr0 {
			// Параллельное добавление заняло адрес — мутатор записи откажет.
			_ = store.UpdateManagedServer("Wireguard1", func(sv *storage.ManagedServer) error {
				sv.Peers = append(sv.Peers, storage.ManagedPeer{PublicKey: "RACE", TunnelIP: "10.66.66.2/32"})
				return nil
			})
		}
	}
	_, err := svc.AddPeer(context.Background(), "Wireguard1", AddPeerRequest{Description: "branch", TunnelIP: "10.66.66.2/32", RemoteSubnets: []string{"192.168.77.0/24"}})
	if err == nil || !strings.Contains(err.Error(), "save to storage") {
		t.Fatalf("err = %v", err)
	}
	if got, want := aclParses(poster), []string{acl1 + net77 + segBr0, acl1Off + net77 + segBr0}; !slices.Equal(got, want) {
		t.Fatalf("acl = %q, want %q", got, want)
	}
}
