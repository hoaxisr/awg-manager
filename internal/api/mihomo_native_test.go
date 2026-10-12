package api

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/hoaxisr/awg-manager/internal/mihomonative"
)

func TestMihomoNativeApplyFalsePreparesButDoesNotPublishBridge(t *testing.T) {
	store, err := mihomonative.NewStore(filepath.Join(t.TempDir(), "native.json"))
	if err != nil {
		t.Fatal(err)
	}
	nodes, err := store.CreateProxy("vless://id@host:443?type=xhttp#Deferred", mihomonative.EngineMihomo, mihomonative.EngineMihomo)
	if err != nil {
		t.Fatal(err)
	}
	h := NewMihomoHandler(&fakeMihomoEngine{})
	h.SetNativeStore(store)
	prepare, ready, down := 0, 0, 0
	h.SetNativeBridgeLifecycle(
		func(context.Context, []mihomonative.BridgeRef) error { prepare++; return nil },
		func(context.Context) error { ready++; return nil },
		func(context.Context) error { down++; return nil },
	)
	reloads := 0
	h.SetReloadFunc(func() error { reloads++; return nil })
	mux := http.NewServeMux()
	h.RegisterRoutes(mux, nil)

	rec := httptest.NewRecorder()
	path := "/api/mihomo/native/proxies/" + nodes[0].ID + "?apply=false"
	mux.ServeHTTP(rec, httptest.NewRequest(http.MethodDelete, path, nil))
	if rec.Code != http.StatusOK {
		t.Fatalf("status=%d body=%s", rec.Code, rec.Body.String())
	}
	if prepare != 1 || ready != 0 || down != 0 || reloads != 0 {
		t.Fatalf("lifecycle prepare=%d ready=%d down=%d reload=%d", prepare, ready, down, reloads)
	}
}

func TestMihomoNativePublishesBridgeOnlyAfterSuccessfulApply(t *testing.T) {
	store, err := mihomonative.NewStore(filepath.Join(t.TempDir(), "native.json"))
	if err != nil {
		t.Fatal(err)
	}
	h := NewMihomoHandler(&fakeMihomoEngine{})
	h.SetNativeStore(store)
	h.SetSettingsStore(newMihomoHandlerSettings(t, "mihomo", true))
	events := make([]string, 0, 3)
	h.SetNativeBridgeLifecycle(
		func(context.Context, []mihomonative.BridgeRef) error { events = append(events, "prepare"); return nil },
		func(context.Context) error { events = append(events, "ready"); return nil },
		func(context.Context) error { events = append(events, "down"); return nil },
	)
	h.SetReloadFunc(func() error { events = append(events, "apply"); return nil })
	mux := http.NewServeMux()
	h.RegisterRoutes(mux, nil)

	body := []byte(`{"uri":"vless://id@host:443?type=xhttp#Ordered","enginePreference":"mihomo"}`)
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, httptest.NewRequest(http.MethodPost, "/api/mihomo/native/proxies", bytes.NewReader(body)))
	if rec.Code != http.StatusOK {
		t.Fatalf("status=%d body=%s", rec.Code, rec.Body.String())
	}
	if got := strings.Join(events, ","); got != "prepare,apply,ready" {
		t.Fatalf("lifecycle order=%q", got)
	}
}

func TestMihomoNativeExternalLifecycleDeleteReceivesOldBridge(t *testing.T) {
	store, err := mihomonative.NewStore(filepath.Join(t.TempDir(), "native.json"))
	if err != nil {
		t.Fatal(err)
	}
	nodes, err := store.CreateProxy("vless://id@host:443?type=xhttp#Delete", mihomonative.EngineMihomo, mihomonative.EngineMihomo)
	if err != nil {
		t.Fatal(err)
	}
	if err := store.SetBridge("proxy", nodes[0].ID, mihomonative.ProxyBridge{
		ListenPort: 12017, ProxyIndex: 17, ProxyInterface: "Proxy17", KernelInterface: "t2s17",
	}); err != nil {
		t.Fatal(err)
	}
	h := NewMihomoHandler(&fakeMihomoEngine{})
	h.SetNativeStore(store)
	h.SetSettingsStore(newMihomoHandlerSettings(t, "mihomo", true))
	h.SetReloadPublishesNativeBridges(true)
	prepareCalls, readyCalls := 0, 0
	h.SetNativeBridgeLifecycle(
		func(_ context.Context, previous []mihomonative.BridgeRef) error {
			prepareCalls++
			if len(previous) != 1 || previous[0].ID != nodes[0].ID || previous[0].Bridge.ProxyIndex != 17 {
				t.Fatalf("prepare previous=%#v, want deleted bridge", previous)
			}
			return nil
		},
		func(context.Context) error { readyCalls++; return nil },
		func(context.Context) error { return nil },
	)
	h.SetReloadFunc(func() error { return nil })
	mux := http.NewServeMux()
	h.RegisterRoutes(mux, nil)

	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, httptest.NewRequest(http.MethodDelete, "/api/mihomo/native/proxies/"+nodes[0].ID, nil))
	if rec.Code != http.StatusOK {
		t.Fatalf("status=%d body=%s", rec.Code, rec.Body.String())
	}
	if prepareCalls != 1 || readyCalls != 0 {
		t.Fatalf("external lifecycle prepare=%d handler-ready=%d", prepareCalls, readyCalls)
	}
}

func TestMihomoNativeProxyAPI(t *testing.T) {
	store, err := mihomonative.NewStore(filepath.Join(t.TempDir(), "native.json"))
	if err != nil {
		t.Fatal(err)
	}
	h := NewMihomoHandler(nil)
	h.SetNativeStore(store)
	mux := http.NewServeMux()
	h.RegisterRoutes(mux, nil)
	body := []byte(`{"uri":"vless://id@host:443?type=xhttp#X","enginePreference":"auto"}`)
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, httptest.NewRequest(http.MethodPost, "/api/mihomo/native/proxies", bytes.NewReader(body)))
	if rec.Code != http.StatusOK {
		t.Fatalf("status=%d body=%s", rec.Code, rec.Body.String())
	}
	var envelope struct {
		Success bool `json:"success"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &envelope); err != nil || !envelope.Success {
		t.Fatalf("body=%s err=%v", rec.Body.String(), err)
	}
	rec = httptest.NewRecorder()
	mux.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/api/mihomo/native/proxies", nil))
	if rec.Code != http.StatusOK || !bytes.Contains(rec.Body.Bytes(), []byte(`"selectedEngine":"mihomo"`)) {
		t.Fatalf("status=%d body=%s", rec.Code, rec.Body.String())
	}
}

func TestMihomoNativeManualProxyAPI(t *testing.T) {
	store, err := mihomonative.NewStore(filepath.Join(t.TempDir(), "native.json"))
	if err != nil {
		t.Fatal(err)
	}
	h := NewMihomoHandler(nil)
	h.SetNativeStore(store)
	mux := http.NewServeMux()
	h.RegisterRoutes(mux, nil)
	body := []byte(`{"enginePreference":"mihomo","manual":{"name":"HY2","protocol":"hysteria2","server":"hy.example","port":443,"config":{"password":"secret","sni":"edge.example"}}}`)
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, httptest.NewRequest(http.MethodPost, "/api/mihomo/native/proxies", bytes.NewReader(body)))
	if rec.Code != http.StatusOK || len(store.ListProxies()) != 1 {
		t.Fatalf("status=%d body=%s", rec.Code, rec.Body.String())
	}
}

func TestMihomoNativeProxyDeleteRollsBackWhenApplyFails(t *testing.T) {
	store, err := mihomonative.NewStore(filepath.Join(t.TempDir(), "native.json"))
	if err != nil {
		t.Fatal(err)
	}
	nodes, err := store.CreateProxy("vless://id@host:443?type=xhttp#Keep", mihomonative.EngineMihomo, mihomonative.EngineMihomo)
	if err != nil {
		t.Fatal(err)
	}
	h := NewMihomoHandler(&fakeMihomoEngine{})
	h.SetNativeStore(store)
	h.SetSettingsStore(newMihomoHandlerSettings(t, "mihomo", true))
	reloadCalls := 0
	h.SetReloadFunc(func() error {
		reloadCalls++
		return errors.New("validation failed")
	})
	mux := http.NewServeMux()
	h.RegisterRoutes(mux, nil)

	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, httptest.NewRequest(http.MethodDelete, "/api/mihomo/native/proxies/"+nodes[0].ID, nil))
	if rec.Code != http.StatusInternalServerError {
		t.Fatalf("status=%d body=%s", rec.Code, rec.Body.String())
	}
	got := store.ListProxies()
	if len(got) != 1 || got[0].ID != nodes[0].ID {
		t.Fatalf("proxies after rollback=%#v", got)
	}
	if reloadCalls != 2 {
		t.Fatalf("reload calls=%d, want failed apply and rollback apply", reloadCalls)
	}
}

func TestMihomoNativeProxyGetAndUpdateAPI(t *testing.T) {
	store, err := mihomonative.NewStore(filepath.Join(t.TempDir(), "native.json"))
	if err != nil {
		t.Fatal(err)
	}
	nodes, err := store.CreateProxy("vless://id@host:443?type=xhttp#Before", mihomonative.EngineMihomo, mihomonative.EngineMihomo)
	if err != nil {
		t.Fatal(err)
	}
	h := NewMihomoHandler(&fakeMihomoEngine{})
	h.SetNativeStore(store)
	h.SetSettingsStore(newMihomoHandlerSettings(t, "mihomo", true))
	h.SetReloadFunc(func() error { return nil })
	mux := http.NewServeMux()
	h.RegisterRoutes(mux, nil)

	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/api/mihomo/native/proxies/"+nodes[0].ID, nil))
	if rec.Code != http.StatusOK || !bytes.Contains(rec.Body.Bytes(), []byte(`"name":"Before"`)) {
		t.Fatalf("GET status=%d body=%s", rec.Code, rec.Body.String())
	}

	body := []byte(`{"uri":"vless://id@host:443?type=xhttp#After","enginePreference":"mihomo","enabled":true}`)
	rec = httptest.NewRecorder()
	mux.ServeHTTP(rec, httptest.NewRequest(http.MethodPut, "/api/mihomo/native/proxies/"+nodes[0].ID, bytes.NewReader(body)))
	if rec.Code != http.StatusOK || !bytes.Contains(rec.Body.Bytes(), []byte(`"name":"After"`)) {
		t.Fatalf("PUT status=%d body=%s", rec.Code, rec.Body.String())
	}
	updated, err := store.GetProxy(nodes[0].ID)
	if err != nil || updated.Name != "After" || updated.ID != nodes[0].ID {
		t.Fatalf("updated=%#v err=%v", updated, err)
	}
}

func TestMihomoNativeSubscriptionUpdateRollsBackWhenApplyFails(t *testing.T) {
	store, err := mihomonative.NewStore(filepath.Join(t.TempDir(), "native.json"))
	if err != nil {
		t.Fatal(err)
	}
	sub, err := store.CreateSubscription(mihomonative.CreateSubscriptionInput{
		Name: "Before", URL: "https://example.test/sub.yaml",
		Format: mihomonative.FormatMihomoProvider, EnginePreference: mihomonative.EngineMihomo,
		RefreshHours: 24, Enabled: true, RoutingEngine: mihomonative.EngineMihomo,
	})
	if err != nil {
		t.Fatal(err)
	}
	h := NewMihomoHandler(&fakeMihomoEngine{})
	h.SetNativeStore(store)
	h.SetSettingsStore(newMihomoHandlerSettings(t, "mihomo", true))
	reloadCalls := 0
	h.SetReloadFunc(func() error {
		reloadCalls++
		return errors.New("invalid generated config")
	})
	mux := http.NewServeMux()
	h.RegisterRoutes(mux, nil)

	body := []byte(`{"name":"After","url":"https://example.test/new.yaml","format":"mihomo-provider","enginePreference":"mihomo","refreshHours":12,"enabled":true}`)
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, httptest.NewRequest(http.MethodPut, "/api/mihomo/native/subscriptions/"+sub.ID, bytes.NewReader(body)))
	if rec.Code != http.StatusInternalServerError {
		t.Fatalf("PUT status=%d body=%s", rec.Code, rec.Body.String())
	}
	restored, err := store.GetSubscription(sub.ID)
	if err != nil || restored.Name != "Before" || restored.URL != "https://example.test/sub.yaml" {
		t.Fatalf("restored=%#v err=%v", restored, err)
	}
	if reloadCalls != 2 {
		t.Fatalf("reload calls=%d, want failed apply plus rollback apply", reloadCalls)
	}
}

func TestMihomoNativeSubscriptionUpdateMigratesLegacyAutoFormat(t *testing.T) {
	store, err := mihomonative.NewStore(filepath.Join(t.TempDir(), "native.json"))
	if err != nil {
		t.Fatal(err)
	}
	sub, err := store.CreateSubscription(mihomonative.CreateSubscriptionInput{
		Name: "Legacy", URL: "https://example.test/legacy.yaml",
		Format: mihomonative.FormatAuto, EnginePreference: mihomonative.EngineMihomo,
		RefreshHours: 24, Enabled: true, RoutingEngine: mihomonative.EngineMihomo,
	})
	if err != nil {
		t.Fatal(err)
	}

	h := NewMihomoHandler(nil)
	h.SetNativeStore(store)
	mux := http.NewServeMux()
	h.RegisterRoutes(mux, nil)
	body := []byte(`{"name":"Legacy","url":"https://example.test/current.yaml","format":"mihomo-provider","enginePreference":"mihomo","refreshHours":12,"enabled":true}`)
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, httptest.NewRequest(http.MethodPut, "/api/mihomo/native/subscriptions/"+sub.ID, bytes.NewReader(body)))
	if rec.Code != http.StatusOK {
		t.Fatalf("PUT status=%d body=%s", rec.Code, rec.Body.String())
	}
	updated, err := store.GetSubscription(sub.ID)
	if err != nil {
		t.Fatal(err)
	}
	if updated.Format != mihomonative.FormatMihomoProvider || updated.URL != "https://example.test/current.yaml" {
		t.Fatalf("updated=%#v", updated)
	}
}

func TestMihomoNativeGroupUpdateRollsBackWhenApplyFails(t *testing.T) {
	store, err := mihomonative.NewStore(filepath.Join(t.TempDir(), "native.json"))
	if err != nil {
		t.Fatal(err)
	}
	group, err := store.SaveGroup(mihomonative.ProxyGroup{
		Name: "Before", Type: "select", Proxies: []string{"DIRECT"}, Enabled: true,
	})
	if err != nil {
		t.Fatal(err)
	}
	h := NewMihomoHandler(&fakeMihomoEngine{})
	h.SetNativeStore(store)
	h.SetSettingsStore(newMihomoHandlerSettings(t, "mihomo", true))
	reloadCalls := 0
	h.SetReloadFunc(func() error {
		reloadCalls++
		return errors.New("invalid generated config")
	})
	mux := http.NewServeMux()
	h.RegisterRoutes(mux, nil)

	body := []byte(`{"name":"After","type":"select","proxies":["DIRECT"],"enabled":true}`)
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, httptest.NewRequest(http.MethodPut, "/api/mihomo/native/groups/"+group.ID, bytes.NewReader(body)))
	if rec.Code != http.StatusInternalServerError {
		t.Fatalf("PUT status=%d body=%s", rec.Code, rec.Body.String())
	}
	groups := store.ListGroups()
	if len(groups) != 1 || groups[0].ID != group.ID || groups[0].Name != "Before" {
		t.Fatalf("groups after rollback=%#v", groups)
	}
	if reloadCalls != 2 {
		t.Fatalf("reload calls=%d, want failed apply plus rollback apply", reloadCalls)
	}
}

func TestMihomoNativeGroupDeleteRoute(t *testing.T) {
	store, err := mihomonative.NewStore(filepath.Join(t.TempDir(), "native.json"))
	if err != nil {
		t.Fatal(err)
	}
	group, err := store.SaveGroup(mihomonative.ProxyGroup{
		Name:    "DeleteMe",
		Type:    "url-test",
		Proxies: []string{"DIRECT"},
		Enabled: true,
	})
	if err != nil {
		t.Fatal(err)
	}

	h := NewMihomoHandler(&fakeMihomoEngine{})
	h.SetNativeStore(store)
	h.SetSettingsStore(newMihomoHandlerSettings(t, "mihomo", true))
	var reloaded bool
	h.SetReloadFunc(func() error {
		reloaded = true
		return nil
	})

	mux := http.NewServeMux()
	h.RegisterRoutes(mux, nil)

	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodDelete, "/api/mihomo/native/groups/"+group.ID, nil)
	mux.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("DELETE status=%d body=%s", rec.Code, rec.Body.String())
	}
	if !reloaded {
		t.Fatal("expected reload to be called on group delete")
	}
	groups := store.ListGroups()
	if len(groups) != 0 {
		t.Fatalf("expected 0 groups, got: %+v", groups)
	}
}


func TestMihomoNativeConfigMutationsShareTransactionMutex(t *testing.T) {
	store, err := mihomonative.NewStore(filepath.Join(t.TempDir(), "native.json"))
	if err != nil {
		t.Fatal(err)
	}
	h := NewMihomoHandler(&fakeMihomoEngine{})
	h.SetNativeStore(store)
	h.SetSettingsStore(newMihomoHandlerSettings(t, "mihomo", true))

	firstReloadEntered := make(chan struct{})
	releaseFirstReload := make(chan struct{})
	defer func() {
		select {
		case <-releaseFirstReload:
		default:
			close(releaseFirstReload)
		}
	}()
	var reloadCalls atomic.Int32
	h.SetReloadFunc(func() error {
		if reloadCalls.Add(1) == 1 {
			close(firstReloadEntered)
			<-releaseFirstReload
		}
		return nil
	})
	mux := http.NewServeMux()
	h.RegisterRoutes(mux, nil)

	groupDone := make(chan *httptest.ResponseRecorder, 1)
	go func() {
		rec := httptest.NewRecorder()
		body := bytes.NewBufferString(`{"name":"Serialized","type":"select","proxies":["DIRECT"],"enabled":true}`)
		mux.ServeHTTP(rec, httptest.NewRequest(http.MethodPost, "/api/mihomo/native/groups", body))
		groupDone <- rec
	}()

	select {
	case <-firstReloadEntered:
	case <-time.After(2 * time.Second):
		t.Fatal("first group mutation did not reach reload")
	}

	ruleStarted := make(chan struct{})
	ruleDone := make(chan *httptest.ResponseRecorder, 1)
	go func() {
		close(ruleStarted)
		rec := httptest.NewRecorder()
		body := bytes.NewBufferString(`{"type":"MATCH","outbound":"DIRECT","enabled":true}`)
		mux.ServeHTTP(rec, httptest.NewRequest(http.MethodPost, "/api/mihomo/native/rules", body))
		ruleDone <- rec
	}()
	<-ruleStarted

	select {
	case rec := <-ruleDone:
		t.Fatalf("rule mutation escaped native transaction mutex: status=%d body=%s", rec.Code, rec.Body.String())
	case <-time.After(100 * time.Millisecond):
		// Expected: the group mutation still owns nativeMu during reload.
	}

	close(releaseFirstReload)
	for name, done := range map[string]<-chan *httptest.ResponseRecorder{
		"group": groupDone,
		"rule":  ruleDone,
	} {
		select {
		case rec := <-done:
			if rec.Code != http.StatusOK {
				t.Fatalf("%s status=%d body=%s", name, rec.Code, rec.Body.String())
			}
		case <-time.After(2 * time.Second):
			t.Fatalf("%s mutation did not finish", name)
		}
	}
	if got := reloadCalls.Load(); got != 2 {
		t.Fatalf("reload calls=%d, want one serialized reload per mutation", got)
	}
}

func TestMihomoHandlerBatchSaveRules_AtomicRollbackOnFailure(t *testing.T) {
	store, err := mihomonative.NewStore(filepath.Join(t.TempDir(), "native.json"))
	if err != nil {
		t.Fatal(err)
	}
	initial, err := store.SaveRule(mihomonative.Rule{
		Type: "DOMAIN-SUFFIX", Payload: "initial.com", Outbound: "DIRECT", Enabled: true,
	})
	if err != nil {
		t.Fatal(err)
	}

	reloads := 0
	h := NewMihomoHandler(&fakeMihomoEngine{})
	h.SetNativeStore(store)
	h.SetSettingsStore(newMihomoHandlerSettings(t, "mihomo", true))
	h.SetReloadFunc(func() error { reloads++; return nil })

	batch := []mihomonative.Rule{
		{Type: "DOMAIN-SUFFIX", Payload: "valid.com", Outbound: "PROXY", Enabled: true},
		{Type: "UNSUPPORTED-TYPE", Payload: "bad.com", Outbound: "PROXY", Enabled: true},
	}

	err = h.BatchSaveRules(context.Background(), batch)
	if err == nil {
		t.Fatal("BatchSaveRules expected error on invalid rule type, got nil")
	}

	if reloads != 0 {
		t.Fatalf("reload calls = %d on failed batch, want 0", reloads)
	}

	rules := store.ListRules()
	if len(rules) != 1 || rules[0].ID != initial.ID {
		t.Fatalf("store was partially mutated by failed batch: %+v", rules)
	}
}

func TestMihomoHandlerMutation_RestoreFailureReturnsCombinedError(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "native.json")
	store, err := mihomonative.NewStore(path)
	if err != nil {
		t.Fatal(err)
	}

	h := NewMihomoHandler(&fakeMihomoEngine{})
	h.SetNativeStore(store)
	h.SetSettingsStore(newMihomoHandlerSettings(t, "mihomo", true))

	_, mutateErr := h.withNativeMutation(context.Background(), false, func() (interface{}, error) {
		_, _ = store.SaveRule(mihomonative.Rule{Type: "MATCH", Outbound: "DIRECT", Enabled: true})
		if err := os.Chmod(dir, 0555); err != nil {
			t.Skip("cannot set directory permissions on this platform")
		}
		return nil, errors.New("simulated business error")
	})
	_ = os.Chmod(dir, 0755)

	if mutateErr == nil {
		t.Fatal("expected mutation error, got nil")
	}
	if !strings.Contains(mutateErr.Error(), "simulated business error") {
		t.Fatalf("expected cause error, got: %v", mutateErr)
	}
}
