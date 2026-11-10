package subscription

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
)

const (
	mlkemTestKey    = "SfGMPfOQXK3Mrf5sVGsQ1nYvCkEW3B3PRwGDUdyGbHk"
	mlkemRealityURL = "vless://3a3b1c2e-9999-4321-aaaa-1234567890ab@h.example:443?type=tcp&security=reality&sni=a.example&fp=chrome&pbk=" + mlkemTestKey + "&sid=ab"
	mlkemTLSURL     = "vless://3a3b1c2e-9999-4321-aaaa-1234567890ac@t.example:443?type=tcp&security=tls&sni=b.example"
)

const mlkemClashBody = `proxies:
  - name: A
    type: vless
    server: h.example
    port: 443
    uuid: 3a3b1c2e-9999-4321-aaaa-1234567890ab
    tls: true
    servername: a.example
    client-fingerprint: chrome
    reality-opts:
      public-key: ` + mlkemTestKey + `
      short-id: ab
`

const mlkemXrayBody = `[{"remarks":"A","outbounds":[{"tag":"proxy","protocol":"vless","settings":{"vnext":[{"address":"h.example","port":443,"users":[{"id":"3a3b1c2e-9999-4321-aaaa-1234567890ab","encryption":"none"}]}]},"streamSettings":{"network":"grpc","grpcSettings":{"serviceName":"xyz"},"security":"reality","realitySettings":{"serverName":"a.example","publicKey":"` + mlkemTestKey + `","fingerprint":"chrome"}}}]}]`

// mlkemStates раскладывает члены подписки по серверу: значение флага в Reality
// (nil — нет); ключа нет — у сервера нет Reality.
func mlkemStates(t *testing.T, mut *fakeMutator, sub *Subscription) map[string]any {
	t.Helper()
	members := make(map[string]bool, len(sub.MemberTags))
	for _, tag := range sub.MemberTags {
		members[tag] = true
	}
	got := map[string]any{}
	for _, ob := range mut.SubscriptionOutbounds() {
		tag, _ := ob["tag"].(string)
		if !members[tag] {
			continue
		}
		server, _ := ob["server"].(string)
		tls, _ := ob["tls"].(map[string]any)
		if reality, ok := tls["reality"].(map[string]any); ok {
			got[server] = reality["support_x25519mlkem768"]
		}
	}
	return got
}

// mlkemFeed — источник подписки: тело меняется на лету, fail роняет ответ,
// hits считает скачивания.
type mlkemFeed struct {
	body atomic.Value
	fail atomic.Bool
	hits atomic.Int32
	url  string
}

func newMLKEMFeed(t *testing.T, body string) *mlkemFeed {
	t.Helper()
	f := &mlkemFeed{}
	f.body.Store(body)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		f.hits.Add(1)
		if f.fail.Load() {
			http.Error(w, "down", http.StatusBadGateway)
			return
		}
		_, _ = w.Write([]byte(f.body.Load().(string)))
	}))
	t.Cleanup(srv.Close)
	f.url = srv.URL
	return f
}

// mlkemFingerprint — отпечаток члена по адресу сервера.
func mlkemFingerprint(t *testing.T, mut *fakeMutator, server string) any {
	t.Helper()
	for _, ob := range mut.SubscriptionOutbounds() {
		if ob["server"] == server {
			tls, _ := ob["tls"].(map[string]any)
			utls, _ := tls["utls"].(map[string]any)
			return utls["fingerprint"]
		}
	}
	t.Fatalf("no outbound for %s", server)
	return nil
}

func mlkemPatch(on bool) UpdatePatch { return UpdatePatch{MLKEMOff: &on} }

func TestMaterializeMember_MLKEMOff(t *testing.T) {
	const firefoxReality = `{"tls":{"utls":{"enabled":true,"fingerprint":"firefox"},"reality":{"enabled":true}}}`
	cases := []struct {
		name            string
		raw             string
		on              bool
		wantFlag        any
		wantFingerprint any
	}{
		{"on sets false, fingerprint kept", firefoxReality, true, false, "firefox"},
		{"on without utls", `{"tls":{"reality":{"enabled":true}}}`, true, false, nil},
		{"off leaves absent", firefoxReality, false, nil, "firefox"},
		{"off keeps provider opt-out", `{"tls":{"utls":{"fingerprint":"firefox"},"reality":{"enabled":true,"support_x25519mlkem768":false}}}`, false, false, "firefox"},
		{"disabled reality untouched", `{"tls":{"utls":{"fingerprint":"firefox"},"reality":{"enabled":false}}}`, true, nil, "firefox"},
		{"plain tls untouched", `{"tls":{"enabled":true,"utls":{"fingerprint":"firefox"}}}`, true, nil, "firefox"},
		{"no tls untouched", `{"type":"shadowsocks"}`, true, nil, nil},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			var ob map[string]any
			if err := json.Unmarshal(materializeMemberOutbound([]byte(tc.raw), "t", "", tc.on, nil), &ob); err != nil {
				t.Fatal(err)
			}
			tls, _ := ob["tls"].(map[string]any)
			reality, _ := tls["reality"].(map[string]any)
			utls, _ := tls["utls"].(map[string]any)
			if got := reality["support_x25519mlkem768"]; got != tc.wantFlag {
				t.Errorf("flag=%v, want %v", got, tc.wantFlag)
			}
			if got := utls["fingerprint"]; got != tc.wantFingerprint {
				t.Errorf("fingerprint=%v, want %v", got, tc.wantFingerprint)
			}
		})
	}
}

// Тела из SubscriptionOutbounds общие с конфигом адаптера: снятие работает с копией.
func TestClearRealityMLKEM(t *testing.T) {
	raw := []byte(`{"tls":{"utls":{"fingerprint":"chrome"},"reality":{"enabled":true,"support_x25519mlkem768":false}}}`)
	got := string(clearRealityMLKEM(raw))
	if strings.Contains(got, "support_x25519mlkem768") {
		t.Fatalf("flag kept: %s", got)
	}
	if !strings.Contains(got, `"fingerprint":"chrome"`) {
		t.Fatalf("fingerprint lost: %s", got)
	}
	if !strings.Contains(string(raw), "support_x25519mlkem768") {
		t.Fatal("source body mutated")
	}
}

// Источник отказа не присылает ни в каком формате: опция подписки обязана
// дойти до тела и пережить обновление.
func TestService_RealityMLKEM_SurvivesRefresh(t *testing.T) {
	for name, body := range map[string]string{
		"share-link": mlkemRealityURL + "#A\n",
		"clash":      mlkemClashBody,
		"xray-json":  mlkemXrayBody,
	} {
		t.Run(name, func(t *testing.T) {
			feed := newMLKEMFeed(t, body)
			svc, mut := newTestService(t)
			ctx := context.Background()
			sub, err := svc.Create(ctx, CreateInput{Label: "s", URL: feed.url, Enabled: true, MLKEMOff: true})
			if err != nil {
				t.Fatal(err)
			}
			check := func(when string) {
				t.Helper()
				if got := mlkemStates(t, mut, sub)["h.example"]; got != false {
					t.Fatalf("%s: flag=%v, want false (sub=%+v)", when, got, sub.MemberTags)
				}
			}
			check("create")
			if _, err := svc.Refresh(ctx, sub.ID); err != nil {
				t.Fatal(err)
			}
			check("refresh")
		})
	}
}

func TestService_RealityMLKEM_OptOutNeedsNoFetch(t *testing.T) {
	feed := newMLKEMFeed(t, mlkemRealityURL+"#A\n"+mlkemTLSURL+"#B\n")
	svc, mut := newTestService(t)
	ctx := context.Background()
	sub, err := svc.Create(ctx, CreateInput{Label: "s", URL: feed.url, Enabled: true})
	if err != nil {
		t.Fatal(err)
	}
	if got := mlkemStates(t, mut, sub)["h.example"]; got != nil {
		t.Fatalf("flag set by default: %v", got)
	}
	before := feed.hits.Load()
	sub, err = svc.Update(sub.ID, mlkemPatch(true))
	if err != nil {
		t.Fatal(err)
	}
	if !sub.MLKEMOff {
		t.Fatal("option not stored")
	}
	if got := mlkemStates(t, mut, sub)["h.example"]; got != false {
		t.Fatalf("flag=%v, want false", got)
	}
	if n := feed.hits.Load() - before; n != 0 {
		t.Fatalf("opt-out fetched the source %d times", n)
	}
}

func TestService_RealityMLKEM_RemovingOptOutRefreshes(t *testing.T) {
	feed := newMLKEMFeed(t, mlkemRealityURL+"#A\n")
	svc, mut := newTestService(t)
	ctx := context.Background()
	sub, err := svc.Create(ctx, CreateInput{Label: "s", URL: feed.url, Enabled: true, MLKEMOff: true})
	if err != nil {
		t.Fatal(err)
	}
	sub, err = svc.Update(sub.ID, mlkemPatch(false))
	if err != nil {
		t.Fatal(err)
	}
	if got := mlkemStates(t, mut, sub)["h.example"]; got != nil {
		t.Fatalf("flag=%v, want removed", got)
	}
}

func TestService_RealityMLKEM_RemovingOptOutKeepsProviderFlag(t *testing.T) {
	feed := newMLKEMFeed(t, mlkemRealityURL+"&support-x25519mlkem768=false#A\n")
	svc, mut := newTestService(t)
	ctx := context.Background()
	sub, err := svc.Create(ctx, CreateInput{Label: "s", URL: feed.url, Enabled: true, MLKEMOff: true})
	if err != nil {
		t.Fatal(err)
	}
	sub, err = svc.Update(sub.ID, mlkemPatch(false))
	if err != nil {
		t.Fatal(err)
	}
	if got := mlkemStates(t, mut, sub)["h.example"]; got != false {
		t.Fatalf("provider opt-out lost: %v", got)
	}
}

// Отпечаток опция не трогает: в форке шара MLKEM есть у chrome, firefox и
// safari, как у Xray.
func TestService_RealityMLKEM_KeepsFingerprint(t *testing.T) {
	const firefox = "vless://3a3b1c2e-9999-4321-aaaa-1234567890ab@h.example:443?type=tcp&security=reality&sni=a.example&fp=firefox&pbk=" + mlkemTestKey + "&sid=ab#A\n"
	feed := newMLKEMFeed(t, firefox)
	svc, mut := newTestService(t)
	sub, err := svc.Create(context.Background(), CreateInput{Label: "s", URL: feed.url, Enabled: true})
	if err != nil {
		t.Fatal(err)
	}
	for _, on := range []bool{true, false} {
		if _, err := svc.Update(sub.ID, mlkemPatch(on)); err != nil {
			t.Fatal(err)
		}
		if got := mlkemFingerprint(t, mut, "h.example"); got != "firefox" {
			t.Fatalf("option %v: fingerprint=%v, want firefox", on, got)
		}
	}
}

// Источник лежит: снять опцию всё равно можно, флаг снимается с сохранённых
// тел.
func TestService_RealityMLKEM_RemovingOptOutWorksWhenSourceDown(t *testing.T) {
	const firefox = "vless://3a3b1c2e-9999-4321-aaaa-1234567890ab@h.example:443?type=tcp&security=reality&sni=a.example&fp=firefox&pbk=" + mlkemTestKey + "&sid=ab#A\n"
	feed := newMLKEMFeed(t, firefox)
	svc, mut := newTestService(t)
	ctx := context.Background()
	sub, err := svc.Create(ctx, CreateInput{Label: "s", URL: feed.url, Enabled: true, MLKEMOff: true})
	if err != nil {
		t.Fatal(err)
	}
	feed.fail.Store(true)
	sub, err = svc.Update(sub.ID, mlkemPatch(false))
	if err != nil {
		t.Fatalf("removing opt-out with source down: %v", err)
	}
	if sub.MLKEMOff {
		t.Fatal("option still on")
	}
	if flag := mlkemStates(t, mut, sub)["h.example"]; flag != nil {
		t.Fatalf("flag=%v, want cleared", flag)
	}
}

// Движок не принял конфиг: запрет MLKEM остаётся в силе.
func TestService_RealityMLKEM_RemovingOptOutRollsBackOnReloadFailure(t *testing.T) {
	svc, mut := newTestService(t)
	sub, err := svc.Create(context.Background(), CreateInput{Label: "i", Inline: mlkemRealityURL + "#A\n", Enabled: true, MLKEMOff: true})
	if err != nil {
		t.Fatal(err)
	}
	mut.reloadErr = errors.New("boom")
	if _, err := svc.Update(sub.ID, mlkemPatch(false)); err == nil {
		t.Fatal("expected reload error")
	}
	got, err := svc.store.Get(sub.ID)
	if err != nil {
		t.Fatal(err)
	}
	if !got.MLKEMOff {
		t.Fatal("option not rolled back")
	}
}

// У inline снятие не идёт через re-parse: добавленный вручную сервер живёт
// только в Members, из исходного текста его не восстановить.
func TestService_RealityMLKEM_InlineKeepsManualMember(t *testing.T) {
	svc, mut := newTestService(t)
	ctx := context.Background()
	sub, err := svc.Create(ctx, CreateInput{Label: "i", Inline: mlkemRealityURL + "#A\n", Enabled: true})
	if err != nil {
		t.Fatal(err)
	}
	manual := strings.ReplaceAll(strings.ReplaceAll(mlkemRealityURL, "h.example", "m.example"), "ab@", "ad@")
	if sub, err = svc.AddManualMember(ctx, sub.ID, manual+"#M"); err != nil {
		t.Fatal(err)
	}
	if sub, err = svc.Update(sub.ID, mlkemPatch(true)); err != nil {
		t.Fatal(err)
	}
	for _, server := range []string{"h.example", "m.example"} {
		if got := mlkemStates(t, mut, sub)[server]; got != false {
			t.Fatalf("%s: flag=%v, want false", server, got)
		}
	}
	if sub, err = svc.Update(sub.ID, mlkemPatch(false)); err != nil {
		t.Fatal(err)
	}
	if len(sub.MemberTags) != 2 {
		t.Fatalf("members=%v, manual server lost", sub.MemberTags)
	}
	got := mlkemStates(t, mut, sub)
	if len(got) != 2 || got["h.example"] != nil || got["m.example"] != nil {
		t.Fatalf("states=%v, want both cleared", got)
	}
}

// Сбой применения откатывает и опцию: она в общем rollbackPatch.
func TestService_RealityMLKEM_OptOutRollsBackOnReloadFailure(t *testing.T) {
	feed := newMLKEMFeed(t, mlkemRealityURL+"#A\n")
	svc, mut := newTestService(t)
	sub, err := svc.Create(context.Background(), CreateInput{Label: "s", URL: feed.url, Enabled: true})
	if err != nil {
		t.Fatal(err)
	}
	mut.reloadErr = errors.New("boom")
	if _, err := svc.Update(sub.ID, mlkemPatch(true)); err == nil {
		t.Fatal("expected reload error")
	}
	got, err := svc.store.Get(sub.ID)
	if err != nil {
		t.Fatal(err)
	}
	if got.MLKEMOff {
		t.Fatal("option not rolled back")
	}
}

// У xhttp-загрузки свой reality и свой ClientHello: опция обязана дойти и до него.
func TestRealityMLKEM_XHTTPDownload(t *testing.T) {
	raw := []byte(`{"tls":{"reality":{"enabled":true}},"transport":{"type":"xhttp","download":{"tls":{"reality":{"enabled":true}}}}}`)
	var ob map[string]any
	if err := json.Unmarshal(materializeMemberOutbound(raw, "t", "", true, nil), &ob); err != nil {
		t.Fatal(err)
	}
	download := ob["transport"].(map[string]any)["download"].(map[string]any)
	for name, holder := range map[string]map[string]any{"outbound": ob, "download": download} {
		reality := holder["tls"].(map[string]any)["reality"].(map[string]any)
		if reality["support_x25519mlkem768"] != false {
			t.Errorf("%s: flag=%v", name, reality["support_x25519mlkem768"])
		}
	}
	if got := string(clearRealityMLKEM(materializeMemberOutbound(raw, "t", "", true, nil))); strings.Contains(got, "support_x25519mlkem768") {
		t.Fatalf("flag kept: %s", got)
	}
}
