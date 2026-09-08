package api

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/hoaxisr/awg-manager/internal/managed"
	"github.com/hoaxisr/awg-manager/internal/ndms"
	ndmscommand "github.com/hoaxisr/awg-manager/internal/ndms/command"
	"github.com/hoaxisr/awg-manager/internal/ndms/query"
	"github.com/hoaxisr/awg-manager/internal/signature"
	"github.com/hoaxisr/awg-manager/internal/storage"
)

func TestPeerTunnelIPInUse(t *testing.T) {
	server := &ndms.WireguardServer{
		Peers: []ndms.WireguardServerPeer{
			{PublicKey: "A=", AllowedIPs: []string{"10.0.0.20/32"}},
			{PublicKey: "B=", AllowedIPs: []string{"10.0.0.3/32"}},
		},
	}
	tests := []struct {
		name     string
		tunnelIP string
		want     bool
	}{
		// Regression: "10.0.0.2" is a string prefix of "10.0.0.20" but a
		// distinct host — the old HasPrefix check wrongly reported it in use.
		{"prefix-overlap is free", "10.0.0.2/32", false},
		{"exact match is in use", "10.0.0.20/32", true},
		{"other exact match in use", "10.0.0.3/32", true},
		{"unrelated free", "10.0.0.99/32", false},
		{"invalid input is not in use", "garbage", false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := peerTunnelIPInUse(server, tt.tunnelIP); got != tt.want {
				t.Errorf("peerTunnelIPInUse(%q) = %v, want %v", tt.tunnelIP, got, tt.want)
			}
		})
	}
}

// peerFixturePubKey — публичный ключ, который отдаёт шов genKeyPair в тестах ниже.
// AddServerPeer проверяет его isValidWGKey (44 символа base64 → 32 байта), поэтому
// литерал обязан быть валидным по форме.
const peerFixturePubKey = "AB/CD+EF" + "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa" + "="

// stubPeerKeygen подменяет швы генерации ключей: без этого AddServerPeer
// зовёт /opt/sbin/awg на хосте.
func stubPeerKeygen(t *testing.T) {
	t.Helper()
	oldPair, oldPSK := genKeyPair, genPSK
	genKeyPair = func(context.Context) (string, string, error) {
		return "PRIV-fixture", peerFixturePubKey, nil
	}
	genPSK = func(context.Context) (string, error) { return "PSK-fixture", nil }
	t.Cleanup(func() { genKeyPair, genPSK = oldPair, oldPSK })
}

func postServerPeer(t *testing.T, h *ServersHandler, body string) *httptest.ResponseRecorder {
	t.Helper()
	req := httptest.NewRequest(http.MethodPost, "/api/servers/Wireguard0/peers", strings.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	rr := httptest.NewRecorder()
	h.AddServerPeer(rr, req, "Wireguard0")
	return rr
}

// Швы обязаны по умолчанию указывать на прод-генераторы: подмена дефолта
// обёрткой означала бы, что прод ходит не туда, куда тест думает.
func TestServerPeerSeams_DefaultToProduction(t *testing.T) {
	if reflect.ValueOf(genKeyPair).Pointer() != reflect.ValueOf(managed.GenerateKeyPair).Pointer() {
		t.Fatal("genKeyPair по умолчанию обязан быть managed.GenerateKeyPair")
	}
	if reflect.ValueOf(genPSK).Pointer() != reflect.ValueOf(managed.GeneratePresharedKey).Pointer() {
		t.Fatal("genPSK по умолчанию обязан быть managed.GeneratePresharedKey")
	}
}

// Успешное добавление: сгенерированные ключи уходят и в payload NDMS, и в
// стор секретов, публикация ровно одна.
func TestServersHandler_AddServerPeer_StoresSecretAndPublishes(t *testing.T) {
	h, store, poster, p, _ := newServersNATHarness(t)
	stubPeerKeygen(t)

	rr := postServerPeer(t, h, `{"description":"Phone","tunnelIP":"10.9.0.7/32"}`)
	if rr.Code != 200 {
		t.Fatalf("code=%d body=%s", rr.Code, rr.Body.String())
	}
	joined := strings.Join(poster.snapshot(), "\n")
	for _, want := range []string{
		`"key":"` + peerFixturePubKey + `"`,
		`"preshared-key":"PSK-fixture"`,
		`"comment":"Phone"`,
		`"address":"10.9.0.7"`,
		// Пир добавляется сразу включённым: без connect:true запись легла бы в
		// конфиг роутера мёртвой, и клиент не подключился бы молча.
		`"connect":true`,
	} {
		if !strings.Contains(joined, want) {
			t.Fatalf("в payload NDMS нет %s:\n%s", want, joined)
		}
	}
	sec, ok := store.GetServerPeerSecret("Wireguard0", peerFixturePubKey)
	// Сигнатура — предмет TestServersHandler_AddServerPeer_GeneratesSignature;
	// здесь сверяем ключевой материал, чтобы тест не переписывался при каждом
	// изменении генератора.
	if !ok || sec.PrivateKey != "PRIV-fixture" || sec.PresharedKey != "PSK-fixture" ||
		sec.Description != "Phone" || sec.TunnelIP != "10.9.0.7/32" {
		t.Fatalf("секрет = %+v (ok=%v)", sec, ok)
	}
	if got, want := p.invalidated(), []string{"servers/server-peer-added"}; !reflect.DeepEqual(got, want) {
		t.Fatalf("публикации = %v, want %v", got, want)
	}
}

// Отказ NDMS: секрет обязан быть записан ДО обращения к роутеру (иначе
// приватный ключ принятого пира терялся бы навсегда) и снят после отказа
// (иначе в сторе копился бы мусор под ключами, которых на роутере нет).
func TestServersHandler_AddServerPeer_RollsBackSecretOnNDMSRefusal(t *testing.T) {
	h, store, poster, p, _ := newServersNATHarness(t)
	stubPeerKeygen(t)

	secretVisibleDuringRCI := false
	poster.setFailOn(func(payload string) error {
		if !strings.Contains(payload, `"peer"`) {
			return nil
		}
		_, secretVisibleDuringRCI = store.GetServerPeerSecret("Wireguard0", peerFixturePubKey)
		return errors.New("router refused")
	})

	rr := postServerPeer(t, h, `{"description":"Phone","tunnelIP":"10.9.0.7/32"}`)
	if got := decodeJSONBody(t, rr)["code"]; got != "ADD_PEER_FAILED" {
		t.Fatalf("code = %v, want ADD_PEER_FAILED (body=%s)", got, rr.Body.String())
	}
	if !secretVisibleDuringRCI {
		t.Fatal("на момент запроса к роутеру секрет обязан уже лежать в сторе")
	}
	if _, ok := store.GetServerPeerSecret("Wireguard0", peerFixturePubKey); ok {
		t.Fatal("после отказа NDMS секрет обязан быть откачен")
	}
	if pub := p.invalidated(); len(pub) != 0 {
		t.Fatalf("на отказе публикаций быть не должно: %v", pub)
	}
}

// Отказ генерации ключей: ни RCI, ни секрета, ни публикации.
func TestServersHandler_AddServerPeer_KeygenFailureStopsBeforeRCI(t *testing.T) {
	h, store, poster, p, _ := newServersNATHarness(t)
	stubPeerKeygen(t)
	genKeyPair = func(context.Context) (string, string, error) {
		return "", "", errors.New("awg genkey: boom")
	}

	rr := postServerPeer(t, h, `{"description":"Phone","tunnelIP":"10.9.0.7/32"}`)
	if got := decodeJSONBody(t, rr)["code"]; got != "KEYGEN_FAILED" {
		t.Fatalf("code = %v, want KEYGEN_FAILED (body=%s)", got, rr.Body.String())
	}
	if _, ok := store.GetServerPeerSecret("Wireguard0", peerFixturePubKey); ok {
		t.Fatal("секрет не должен появиться при отказе генерации")
	}
	if posts, pub := poster.snapshot(), p.invalidated(); len(posts) != 0 || len(pub) != 0 {
		t.Fatalf("posts=%v pub=%v", posts, pub)
	}
}

// Тот же исход у второго генератора: PSK — отдельный вызов со своей веткой
// ошибки, и её снятие уводило бы пира на роутер с пустым preshared-key.
func TestServersHandler_AddServerPeer_PSKFailureStopsBeforeRCI(t *testing.T) {
	h, store, poster, p, _ := newServersNATHarness(t)
	stubPeerKeygen(t)
	genPSK = func(context.Context) (string, error) {
		return "", errors.New("awg genpsk: boom")
	}

	rr := postServerPeer(t, h, `{"description":"Phone","tunnelIP":"10.9.0.7/32"}`)
	if got := decodeJSONBody(t, rr)["code"]; got != "KEYGEN_FAILED" {
		t.Fatalf("code = %v, want KEYGEN_FAILED (body=%s)", got, rr.Body.String())
	}
	if _, ok := store.GetServerPeerSecret("Wireguard0", peerFixturePubKey); ok {
		t.Fatal("секрет не должен появиться при отказе генерации PSK")
	}
	if posts, pub := poster.snapshot(), p.invalidated(); len(posts) != 0 || len(pub) != 0 {
		t.Fatalf("posts=%v pub=%v", posts, pub)
	}
}

// Фикстура обязана проходить isValidWGKey: AddServerPeer теперь отвергает
// сгенерированный ключ битой формы, и невалидный литерал уводил бы тесты
// успешного пути в отказ вместо успеха.
func TestPeerFixturePubKey_IsValidWGKey(t *testing.T) {
	if !isValidWGKey(peerFixturePubKey) {
		t.Fatalf("фикстура %q не проходит isValidWGKey", peerFixturePubKey)
	}
}

// Шов keygen отдал битый ключ (укороченный base64) — пир НЕ уходит в NDMS и секрет не
// сохраняется: раньше такой ключ доезжал до роутера и стора без проверки.
func TestServersHandler_AddServerPeer_RejectsMalformedGeneratedKey(t *testing.T) {
	h, store, poster, p, _ := newServersNATHarness(t)
	stubPeerKeygen(t)
	oldPair := genKeyPair
	genKeyPair = func(context.Context) (string, string, error) {
		return "PRIV-fixture", "PUB-fixture-0000000000000000000000000=", nil
	}
	t.Cleanup(func() { genKeyPair = oldPair })

	rr := postServerPeer(t, h, `{"tunnelIP":"10.9.0.2/32","description":"phone"}`)
	if rr.Code != http.StatusBadRequest || decodeJSONBody(t, rr)["code"] != "KEYGEN_FAILED" {
		t.Fatalf("code=%d body=%s", rr.Code, rr.Body.String())
	}
	if got := poster.snapshot(); len(got) != 0 {
		t.Fatalf("RCI POST при битом ключе: %v", got)
	}
	if _, ok := store.GetServerPeerSecret("Wireguard0", "PUB-fixture-0000000000000000000000000="); ok {
		t.Fatal("секрет битого ключа сохранён")
	}
	if inv := p.invalidated(); len(inv) != 0 {
		t.Fatalf("публикации при отказе: %v", inv)
	}
}

// newServersPeerHarness — как newServersNATHarness, но с журналом приложения и
// (при seedPeer) с пиром peerFixturePubKey уже в /show/interface/: FakeGetter
// статичен, пир, добавленный через AddServerPeer, в списке не появится.
func newServersPeerHarness(t *testing.T, seedPeer bool) (*ServersHandler, *storage.SettingsStore, *natPoster, *busProbe, *appLogSpy) {
	t.Helper()
	peers := ""
	if seedPeer {
		peers = `,"wireguard":{"peer":[{"public-key":"` + peerFixturePubKey + `","comment":"phone"}]}`
	}
	fg := query.NewFakeGetter()
	fg.SetJSON("/show/interface/", `{"Wireguard0":{"id":"Wireguard0","type":"Wireguard","description":"Wireguard VPN Server","state":"up","link":"up","address":"10.9.0.1","mask":"255.255.255.0"`+peers+`}}`)
	fg.SetJSON("/show/running-config", `{"message":["interface PPPoE0","    ip global 32767","!"]}`)
	queries := query.NewQueries(query.Deps{Getter: fg, Logger: query.NopLogger()})
	poster := &natPoster{}
	store := storage.NewSettingsStore(t.TempDir())
	if _, err := store.Load(); err != nil {
		t.Fatal(err)
	}
	spy := &appLogSpy{}
	h := NewServersHandler(queries, store, nil, spy)
	h.SetCommands(ndmscommand.NewCommands(ndmscommand.Deps{
		Poster:  poster,
		Queries: queries,
		Save:    ndmscommand.NewSaveCoordinator(poster, nil, time.Hour, time.Hour, 0, nil),
	}))
	p := newBusProbe(t)
	h.SetEventBus(p.bus())
	return h, store, poster, p, spy
}

func deleteServerPeer(t *testing.T, h *ServersHandler, pubkey string) *httptest.ResponseRecorder {
	t.Helper()
	req := httptest.NewRequest(http.MethodDelete, "/api/servers/Wireguard0/peers/"+pubkey, nil)
	rr := httptest.NewRecorder()
	h.DeleteServerPeer(rr, req, "Wireguard0", pubkey)
	return rr
}

// breakStoreWrites делает любую запись стора отказной, не трогая хост: файл
// settings.json подменяется непустым каталогом, на который rename отказывает.
func breakStoreWrites(t *testing.T, store *storage.SettingsStore) {
	t.Helper()
	path := filepath.Join(store.DataDir(), "settings.json")
	if err := os.Remove(path); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Join(path, "busy"), 0o755); err != nil {
		t.Fatal(err)
	}
}

// NDMS отверг пира, а откат секрета не смог записать стор — отказ отката раньше
// терялся под `_ =`; теперь он в журнале приложения, ответ остаётся ADD_PEER_FAILED.
func TestServersHandler_AddServerPeer_RollbackFailureIsLogged(t *testing.T) {
	h, store, poster, _, spy := newServersPeerHarness(t, false)
	stubPeerKeygen(t)
	poster.setFailOn(func(payload string) error {
		if strings.Contains(payload, `"peer"`) {
			breakStoreWrites(t, store)
			return errors.New("ndms refused")
		}
		return nil
	})
	rr := postServerPeer(t, h, `{"tunnelIP":"10.9.0.2/32","description":"phone"}`)
	if rr.Code != http.StatusBadRequest || decodeJSONBody(t, rr)["code"] != "ADD_PEER_FAILED" {
		t.Fatalf("code=%d body=%s", rr.Code, rr.Body.String())
	}
	want := "warn|add-peer|rollback of stranded secret failed: "
	if len(spy.entries) != 1 || !strings.HasPrefix(spy.entries[0], want) {
		t.Fatalf("журнал = %v, ждали префикс %q", spy.entries, want)
	}
}

// Пир снят с роутера, но секрет не удалился из стора — ответ 200 (пира на роутере
// уже нет, отказывать нечем), отказ виден в журнале, а не под `_ =`.
func TestServersHandler_DeleteServerPeer_SecretDeleteFailureIsLogged(t *testing.T) {
	h, store, poster, p, spy := newServersPeerHarness(t, true)
	if err := store.SetServerPeerSecret("Wireguard0", peerFixturePubKey, storage.ServerPeerSecret{PrivateKey: "PRIV-fixture"}); err != nil {
		t.Fatal(err)
	}
	poster.setFailOn(func(payload string) error {
		if strings.Contains(payload, `"no":true`) {
			breakStoreWrites(t, store)
		}
		return nil
	})
	rr := deleteServerPeer(t, h, peerFixturePubKey)
	if rr.Code != http.StatusOK {
		t.Fatalf("code=%d body=%s", rr.Code, rr.Body.String())
	}
	want := "warn|delete-peer|peer removed from router but its secret stayed in store: "
	if len(spy.entries) != 1 || !strings.HasPrefix(spy.entries[0], want) {
		t.Fatalf("журнал = %v, ждали префикс %q", spy.entries, want)
	}
	if got := p.invalidated(); len(got) != 1 || got[0] != "servers/server-peer-deleted" {
		t.Fatalf("публикации = %v", got)
	}
}

// harnessServerID — имя сервера, которое сеет newServersPeerHarness/newServersNATHarness.
const harnessServerID = "Wireguard0"

func putServerPeer(t *testing.T, h *ServersHandler, pubkey, body string) *httptest.ResponseRecorder {
	t.Helper()
	req := httptest.NewRequest(http.MethodPut, "/api/servers/"+harnessServerID+"/peers/"+pubkey, strings.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	rr := httptest.NewRecorder()
	h.UpdateServerPeer(rr, req, harnessServerID, pubkey)
	return rr
}

// Сигнатура принадлежит пиру (CONTEXT.md «Сигнатура AWG»): новый пир
// системного сервера получает её сразу, дефолтным профилем.
func TestServersHandler_AddServerPeer_GeneratesSignature(t *testing.T) {
	h, store, _, _, _ := newServersPeerHarness(t, false)
	stubPeerKeygen(t)

	rr := postServerPeer(t, h, `{"description":"phone","tunnelIP":"10.9.0.2/32"}`)
	if rr.Code != http.StatusOK {
		t.Fatalf("code=%d body=%s", rr.Code, rr.Body.String())
	}
	sec, ok := store.GetServerPeerSecret(harnessServerID, peerFixturePubKey)
	if !ok || sec.SignatureProfile != signature.DefaultProfile || !strings.HasPrefix(sec.I1, "<b 0xc") || sec.I2 != "" {
		t.Fatalf("секрет-сигнатура = %+v (ok=%v)", sec, ok)
	}
}

// PUT без поля signature сигнатуру не трогает; с полем — заменяет все пять
// и канонизирует профиль; мусорный профиль и переросшая сигнатура отвергаются
// ДО обращения к роутеру.
func TestServersHandler_UpdateServerPeer_SignatureOptionalAndValidated(t *testing.T) {
	h, store, _, _, _ := newServersPeerHarness(t, true)
	if err := store.SetServerPeerSecret(harnessServerID, peerFixturePubKey, storage.ServerPeerSecret{
		PrivateKey: "k", TunnelIP: "10.9.0.2/32", I1: "<b 0x01>", SignatureProfile: "dns",
	}); err != nil {
		t.Fatal(err)
	}

	if rr := putServerPeer(t, h, peerFixturePubKey, `{"description":"phone"}`); rr.Code != http.StatusOK {
		t.Fatalf("PUT без signature: code=%d body=%s", rr.Code, rr.Body.String())
	}
	sec, _ := store.GetServerPeerSecret(harnessServerID, peerFixturePubKey)
	if sec.I1 != "<b 0x01>" || sec.SignatureProfile != "dns" {
		t.Fatalf("PUT без signature изменил сигнатуру: %+v", sec)
	}

	body := `{"description":"phone","signature":{"profile":" SIP ","i1":"<b 0x02>","i2":"<rc 3>","i3":"","i4":"","i5":""}}`
	if rr := putServerPeer(t, h, peerFixturePubKey, body); rr.Code != http.StatusOK {
		t.Fatalf("PUT с signature: code=%d body=%s", rr.Code, rr.Body.String())
	}
	sec, _ = store.GetServerPeerSecret(harnessServerID, peerFixturePubKey)
	if sec.I1 != "<b 0x02>" || sec.I2 != "<rc 3>" || sec.SignatureProfile != "sip" {
		t.Fatalf("сигнатура после PUT = %+v", sec)
	}

	rr := putServerPeer(t, h, peerFixturePubKey, `{"description":"phone","signature":{"profile":"tls","i1":"<b 0x03>"}}`)
	if rr.Code != http.StatusBadRequest || decodeJSONBody(t, rr)["code"] != "INVALID_SIGNATURE_PROFILE" {
		t.Fatalf("неизвестный профиль: code=%d body=%s", rr.Code, rr.Body.String())
	}

	huge := `{"description":"phone","signature":{"profile":"sip","i1":"` + strings.Repeat("a", 9000) + `"}}`
	rr = putServerPeer(t, h, peerFixturePubKey, huge)
	if rr.Code != http.StatusBadRequest || decodeJSONBody(t, rr)["code"] != "SIGNATURE_TOO_LARGE" {
		t.Fatalf("переросшая сигнатура: code=%d body=%s", rr.Code, rr.Body.String())
	}

	sec, _ = store.GetServerPeerSecret(harnessServerID, peerFixturePubKey)
	if sec.I1 != "<b 0x02>" || sec.SignatureProfile != "sip" {
		t.Fatalf("отвергнутый PUT изменил сигнатуру: %+v", sec)
	}
}

// Пир без локального секрета (создан в веб-интерфейсе Keenetic): сигнатуре
// негде жить — запись отвергается, обычная правка описания работает как
// работала. Секрет без приватного ключа не заводим.
func TestServersHandler_UpdateServerPeer_SignatureWithoutSecret(t *testing.T) {
	h, store, _, _, _ := newServersPeerHarness(t, true)

	body := `{"description":"phone","signature":{"profile":"sip","i1":"<b 0x02>"}}`
	rr := putServerPeer(t, h, peerFixturePubKey, body)
	if rr.Code != http.StatusBadRequest || decodeJSONBody(t, rr)["code"] != "NO_PEER_SECRET" {
		t.Fatalf("code=%d body=%s", rr.Code, rr.Body.String())
	}
	if _, ok := store.GetServerPeerSecret(harnessServerID, peerFixturePubKey); ok {
		t.Fatal("отказ завёл секрет без приватного ключа")
	}

	if rr := putServerPeer(t, h, peerFixturePubKey, `{"description":"laptop"}`); rr.Code != http.StatusOK {
		t.Fatalf("PUT без signature: code=%d body=%s", rr.Code, rr.Body.String())
	}
}

// newServerConfHarness — минимальный обработчик для прямых проверок сборщика
// .conf: из RCI нужен только снимок ASC интерфейса.
func newServerConfHarness(t *testing.T, ascJSON string) *ServersHandler {
	t.Helper()
	fg := query.NewFakeGetter()
	fg.SetJSON("/show/rc/interface/"+harnessServerID+"/wireguard/asc", ascJSON)
	queries := query.NewQueries(query.Deps{Getter: fg, Logger: query.NopLogger()})
	store := storage.NewSettingsStore(t.TempDir())
	if _, err := store.Load(); err != nil {
		t.Fatal(err)
	}
	return NewServersHandler(queries, store, nil, &appLogSpy{})
}

// Сигнатура пира уходит в .conf только когда у сервера есть ASC (Jc > 0):
// без обфускации это обычный WireGuard, и строки I клиент не поймёт. Байты
// берутся из секрета ПИРА, а не из ASC-параметров интерфейса.
func TestServersHandler_GenerateServerPeerConf_SignatureFromPeerAndOnlyWithASC(t *testing.T) {
	sec := storage.ServerPeerSecret{
		PrivateKey: "PRIV", TunnelIP: "10.9.0.2/32",
		I1: "<b 0x01>", I3: "<r 8>", SignatureProfile: "dns",
	}

	t.Run("с ASC", func(t *testing.T) {
		// i1 интерфейса намеренно чужой: в конфиг обязана попасть сигнатура пира.
		h := newServerConfHarness(t, `{"jc":"3","jmin":"8","jmax":"80","s1":"18","s2":"22","h1":"1","h2":"2","h3":"3","h4":"4","i1":"<b 0xdead>"}`)

		server := &ndms.WireguardServer{ID: harnessServerID, ListenPort: 51820, MTU: 1420}
		conf, err := h.generateServerPeerConf(context.Background(), server, peerFixturePubKey, sec, "1.2.3.4")
		if err != nil {
			t.Fatalf("generateServerPeerConf: %v", err)
		}
		for _, want := range []string{"Jc = 3\n", "I1 = <b 0x01>\n", "I3 = <r 8>\n"} {
			if !strings.Contains(conf, want) {
				t.Fatalf("в конфиге нет %q:\n%s", want, conf)
			}
		}
		for _, unwanted := range []string{"0xdead", "I2 ="} {
			if strings.Contains(conf, unwanted) {
				t.Fatalf("в конфиге лишнее %q:\n%s", unwanted, conf)
			}
		}
	})

	t.Run("без ASC", func(t *testing.T) {
		h := newServerConfHarness(t, `{"jc":"0"}`)

		server := &ndms.WireguardServer{ID: harnessServerID, ListenPort: 51820, MTU: 1420}
		conf, err := h.generateServerPeerConf(context.Background(), server, peerFixturePubKey, sec, "1.2.3.4")
		if err != nil {
			t.Fatalf("generateServerPeerConf: %v", err)
		}
		for _, unwanted := range []string{"Jc =", "I1 ="} {
			if strings.Contains(conf, unwanted) {
				t.Fatalf("сервер без ASC получил %q:\n%s", unwanted, conf)
			}
		}
	})
}
