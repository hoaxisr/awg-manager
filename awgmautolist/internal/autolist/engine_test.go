package autolist

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"sync"
	"testing"
	"time"

	"github.com/hoaxisr/awg-manager/awgmautolist/internal/probeq"
)

// fakeAWGM — фейковый awg-manager по реальным контрактам 2.19.25:
//   - GET  /api/singbox/clash/connections -> {connections:[...]}
//   - GET  /api/logs?bucket=singbox       -> {logs:[...]}
//   - GET  /api/singbox/router/rules/list -> {success,data:[...]}
//   - POST /api/singbox/router/rules/add  -> {success:true}
//   - POST /api/singbox/router/staging/apply
//   - POST /api/singbox/router/inspect    -> {success,data:{matches:[]}} (ничего не покрыто)
//   - GET  /api/routing/resolve           -> {success,data:{ips:[...]}}
type fakeAWGM struct {
	mu           sync.Mutex
	srv          *httptest.Server
	rules        []map[string]any
	rulesets     []map[string]any
	rulesetOps   []string // "add:tag" / "update:tag" — хронология операций
	outbounds    []map[string]any
	added        []string // домены в наборе (после последнего update)
	applied      int
	applyNoDraft bool
	logsToGive   []map[string]any
	conns        []map[string]any
	// routedDomains — домены, которые inspect должен считать уже
	// покрытыми не-direct правилом (для теста фильтра покрытия).
	routedDomains map[string]bool
	directDomains map[string]bool
	// resolveIPs — что /routing/resolve отвечает (пусто — пустой список).
	// resolveByDomain — домен → IP; имеет приоритет над resolveIPs.
	resolveIPs      []string
	resolveByDomain map[string][]string
	resolveError    bool

	ruleSetMatches       map[string]bool // input -> match for exclusion tests
	unsupportedRuleSets  map[string]bool
	tunnelDelay          int
	tunnelProbeHTTPError bool
	tunnelProbeCalls     []string
	inspectCalls         int
	ruleSetListCalls     int
	ruleSetListEntered   chan struct{}
	ruleSetListRelease   chan struct{}
	ruleSetListBlockOnce sync.Once
}

func newFakeAWGM(t *testing.T) *fakeAWGM {
	f := &fakeAWGM{
		routedDomains: map[string]bool{}, directDomains: map[string]bool{}, resolveByDomain: map[string][]string{},
		ruleSetMatches: map[string]bool{}, unsupportedRuleSets: map[string]bool{},
	}
	mux := http.NewServeMux()
	mux.HandleFunc("/api/system/all-interfaces", func(w http.ResponseWriter, r *http.Request) {
		json.NewEncoder(w).Encode(map[string]any{"success": true, "data": []map[string]any{
			{"name": "br0", "label": "Home", "up": true, "type": "Bridge", "securityLevel": "private"},
			{"name": "br1", "label": "Down LAN", "up": false, "type": "Bridge", "securityLevel": "private"},
			{"name": "eth3", "label": "WAN", "up": true, "type": "GigabitEthernet", "securityLevel": "public"},
		}})
	})
	mux.HandleFunc("/api/singbox/router/rulesets/match", func(w http.ResponseWriter, r *http.Request) {
		var req struct {
			Input string   `json:"input"`
			Tags  []string `json:"tags"`
		}
		_ = json.NewDecoder(r.Body).Decode(&req)
		f.mu.Lock()
		defer f.mu.Unlock()
		results := make([]map[string]any, 0, len(req.Tags))
		for _, tag := range req.Tags {
			unsupported := f.unsupportedRuleSets[tag]
			result := map[string]any{"tag": tag, "matched": f.ruleSetMatches[req.Input], "supported": !unsupported}
			if unsupported {
				result["error"] = "rule-set unavailable"
			}
			results = append(results, result)
		}
		json.NewEncoder(w).Encode(map[string]any{"success": true, "data": results})
	})
	mux.HandleFunc("/api/singbox/router/inspect", func(w http.ResponseWriter, r *http.Request) {
		var req struct {
			Domain string `json:"domain"`
		}
		_ = json.NewDecoder(r.Body).Decode(&req)
		f.mu.Lock()
		defer f.mu.Unlock()
		f.inspectCalls++
		matched := f.routedDomains[req.Domain] || f.directDomains[req.Domain]
		matchedRule := -1
		m := []map[string]any{}
		if matched {
			matchedRule = 0
			outbound := "existing-vpn"
			if f.directDomains[req.Domain] {
				outbound = "direct"
			}
			m = append(m, map[string]any{
				"index": 0, "matched": true, "action": "route",
				"outbound": outbound, "reason": "совпало по: domain_suffix",
			})
		}
		json.NewEncoder(w).Encode(map[string]any{
			"success": true,
			"data": map[string]any{
				"input": req.Domain, "inputType": "domain",
				"matches": m, "destination": "direct", "final": "direct", "matchedRule": matchedRule,
			},
		})
	})
	mux.HandleFunc("/api/routing/resolve", func(w http.ResponseWriter, r *http.Request) {
		f.mu.Lock()
		defer f.mu.Unlock()
		domain := r.URL.Query().Get("domain")
		if f.resolveError {
			json.NewEncoder(w).Encode(map[string]any{"success": true, "data": map[string]any{"domain": domain, "ips": []string{}, "error": "DNS unavailable"}})
			return
		}
		ips, ok := f.resolveByDomain[domain]
		if !ok {
			ips = f.resolveIPs
		}
		if ips == nil {
			ips = []string{}
		}
		json.NewEncoder(w).Encode(map[string]any{
			"success": true,
			"data":    map[string]any{"domain": domain, "ips": ips},
		})
	})
	mux.HandleFunc("/api/singbox/clash/connections", func(w http.ResponseWriter, r *http.Request) {
		f.mu.Lock()
		defer f.mu.Unlock()
		json.NewEncoder(w).Encode(map[string]any{"connections": f.conns})
	})
	mux.HandleFunc("/api/singbox/clash/proxies/", func(w http.ResponseWriter, r *http.Request) {
		f.mu.Lock()
		defer f.mu.Unlock()
		f.tunnelProbeCalls = append(f.tunnelProbeCalls, r.URL.Query().Get("url"))
		if f.tunnelProbeHTTPError {
			http.Error(w, "tunnel unavailable", http.StatusServiceUnavailable)
			return
		}
		json.NewEncoder(w).Encode(map[string]any{"delay": f.tunnelDelay})
	})
	mux.HandleFunc("/api/logs", func(w http.ResponseWriter, r *http.Request) {
		f.mu.Lock()
		defer f.mu.Unlock()
		if r.URL.Query().Get("bucket") != "singbox" {
			http.Error(w, "bad bucket", 400)
			return
		}
		json.NewEncoder(w).Encode(map[string]any{"logs": f.logsToGive, "total": len(f.logsToGive)})
		// логи выдаются один раз — как свежие
		f.logsToGive = nil
	})
	mux.HandleFunc("/api/singbox/router/outbounds/list", func(w http.ResponseWriter, r *http.Request) {
		f.mu.Lock()
		defer f.mu.Unlock()
		json.NewEncoder(w).Encode(map[string]any{"success": true, "data": f.outbounds})
	})
	mux.HandleFunc("/api/singbox/router/outbounds/add", func(w http.ResponseWriter, r *http.Request) {
		var ob map[string]any
		if err := json.NewDecoder(r.Body).Decode(&ob); err != nil {
			http.Error(w, err.Error(), 400)
			return
		}
		f.mu.Lock()
		defer f.mu.Unlock()
		f.outbounds = append(f.outbounds, ob)
		json.NewEncoder(w).Encode(map[string]any{"success": true})
	})
	mux.HandleFunc("/api/singbox/router/outbounds/update", func(w http.ResponseWriter, r *http.Request) {
		var body struct {
			Tag      string         `json:"tag"`
			Outbound map[string]any `json:"outbound"`
		}
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
			http.Error(w, err.Error(), 400)
			return
		}
		f.mu.Lock()
		defer f.mu.Unlock()
		for i, ob := range f.outbounds {
			if ob["tag"] == body.Tag {
				f.outbounds[i] = body.Outbound
			}
		}
		json.NewEncoder(w).Encode(map[string]any{"success": true})
	})
	mux.HandleFunc("/api/singbox/router/rules/list", func(w http.ResponseWriter, r *http.Request) {
		f.mu.Lock()
		defer f.mu.Unlock()
		json.NewEncoder(w).Encode(map[string]any{"success": true, "data": f.rules})
	})
	// ruleset-эндпоинты (единый набор awgm-autolist)
	mux.HandleFunc("/api/singbox/router/rulesets/list", func(w http.ResponseWriter, r *http.Request) {
		if f.ruleSetListEntered != nil {
			f.ruleSetListBlockOnce.Do(func() { close(f.ruleSetListEntered) })
			if f.ruleSetListRelease != nil {
				<-f.ruleSetListRelease
			}
		}
		f.mu.Lock()
		defer f.mu.Unlock()
		f.ruleSetListCalls++
		json.NewEncoder(w).Encode(map[string]any{"success": true, "data": f.rulesets})
	})
	mux.HandleFunc("/api/singbox/router/rulesets/add", func(w http.ResponseWriter, r *http.Request) {
		var rs map[string]any
		if err := json.NewDecoder(r.Body).Decode(&rs); err != nil {
			http.Error(w, err.Error(), 400)
			return
		}
		f.mu.Lock()
		defer f.mu.Unlock()
		f.rulesets = append(f.rulesets, rs)
		f.rulesetOps = append(f.rulesetOps, "add:"+fmt.Sprint(rs["tag"]))
		// записываем домены из созданного набора (как в update)
		if rules, ok := rs["rules"].([]any); ok {
			for _, rule := range rules {
				if rm, ok := rule.(map[string]any); ok {
					if ds, ok := rm["domain_suffix"].([]any); ok {
						for _, d := range ds {
							f.added = append(f.added, fmt.Sprint(d))
						}
					}
				}
			}
		}
		json.NewEncoder(w).Encode(map[string]any{"success": true})
	})
	mux.HandleFunc("/api/singbox/router/rulesets/update", func(w http.ResponseWriter, r *http.Request) {
		var body struct {
			Tag     string         `json:"tag"`
			RuleSet map[string]any `json:"ruleSet"`
		}
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
			http.Error(w, err.Error(), 400)
			return
		}
		f.mu.Lock()
		defer f.mu.Unlock()
		replaced := false
		for i, rs := range f.rulesets {
			if rs["tag"] == body.Tag {
				f.rulesets[i] = body.RuleSet
				replaced = true
				break
			}
		}
		if !replaced {
			f.rulesets = append(f.rulesets, body.RuleSet)
		}
		f.rulesetOps = append(f.rulesetOps, "update:"+body.Tag)
		// записываем домены из набора (для проверок тестов)
		if rules, ok := body.RuleSet["rules"].([]any); ok {
			f.added = nil
			for _, rule := range rules {
				if rm, ok := rule.(map[string]any); ok {
					if ds, ok := rm["domain_suffix"].([]any); ok {
						for _, d := range ds {
							f.added = append(f.added, fmt.Sprint(d))
						}
					}
				}
			}
		}
		json.NewEncoder(w).Encode(map[string]any{"success": true})
	})
	mux.HandleFunc("/api/singbox/router/rules/add", func(w http.ResponseWriter, r *http.Request) {
		var rule map[string]any
		if err := json.NewDecoder(r.Body).Decode(&rule); err != nil {
			http.Error(w, err.Error(), 400)
			return
		}
		f.mu.Lock()
		defer f.mu.Unlock()
		f.rules = append(f.rules, rule)
		if ds, ok := rule["domain_suffix"].([]any); ok {
			for _, d := range ds {
				f.added = append(f.added, fmt.Sprint(d))
			}
		}
		json.NewEncoder(w).Encode(map[string]any{"success": true})
	})
	mux.HandleFunc("/api/singbox/router/rules/update", func(w http.ResponseWriter, r *http.Request) {
		var body struct {
			Index int            `json:"index"`
			Rule  map[string]any `json:"rule"`
		}
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
			http.Error(w, err.Error(), 400)
			return
		}
		f.mu.Lock()
		defer f.mu.Unlock()
		if body.Index < 0 || body.Index >= len(f.rules) {
			http.Error(w, "rule index out of range", 400)
			return
		}
		f.rules[body.Index] = body.Rule
		json.NewEncoder(w).Encode(map[string]any{"success": true})
	})
	mux.HandleFunc("/api/singbox/router/staging/apply", func(w http.ResponseWriter, r *http.Request) {
		f.mu.Lock()
		defer f.mu.Unlock()
		if f.applyNoDraft {
			w.WriteHeader(http.StatusConflict)
			json.NewEncoder(w).Encode(map[string]any{"error": true, "message": "no draft to apply", "code": "NO_DRAFT"})
			return
		}
		f.applied++
		json.NewEncoder(w).Encode(map[string]any{"success": true})
	})
	f.srv = httptest.NewServer(mux)
	t.Cleanup(f.srv.Close)
	return f
}

// resetConns очищает накопленные соединения (для тестов, где каждый вызов
// PollConnections должен видеть только текущий срез).
func (f *fakeAWGM) resetConns() {
	f.mu.Lock()
	f.conns = nil
	f.mu.Unlock()
}

func (f *fakeAWGM) pushDialLog(host, port string) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.logsToGive = append(f.logsToGive, map[string]any{
		"timestamp": time.Now().UTC().Format(time.RFC3339Nano),
		"level":     "error",
		"group":     "singbox",
		"subgroup":  "runtime",
		"message": fmt.Sprintf("connection: open connection to %s:%s using outbound/direct[direct]: dial tcp %s:%s: i/o timeout",
			host, port, host, port),
	})
}

func (f *fakeAWGM) pushStallConn(host, ip, port string, up, down int64) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.conns = append(f.conns, map[string]any{
		"metadata": map[string]any{
			"network": "tcp", "sourceIP": "192.168.31.10",
			"destinationIP": ip, "destinationPort": port, "host": host,
		},
		"upload": up, "download": down,
		"start":    time.Now().Add(-30 * time.Second).UTC().Format(time.RFC3339),
		"outbound": "direct",
		"chains":   []string{"direct"},
	})
}

// blockingProbeServer — целевой «сайт»: соединение принимается, TLS — нет
// (сервер рвёт сразу после connect), имитируя SNI-блок.
func TestEngineFullLoop(t *testing.T) {
	awgm := newFakeAWGM(t)

	// Живой «замороженный» сайт: отвечает малым телом и повисает —
	// используется в stall-срезах ниже.
	blocked := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(200)
	}))
	_ = blocked // сервер жив, его адрес в лог не попадает (dial-тест на домене)

	// Проба «blocked» через подменный транспорт в Prober невозможна из движка
	// (движок создаёт Prober сам), поэтому проверяем цикл до уровня очереди
	// и правил, а вердикты тестируем в autolist_test.go.

	// 1. dial-ошибка на ДОМЕН трижды -> кандидат (raw приватный IP без
	// SNI-подсказки в кандидаты не попадает — это правильно)
	for i := 0; i < DefaultDialFailThreshold; i++ {
		awgm.pushDialLog("dead-site.example", "443")
	}

	eng := NewEngine(awgm.srv.URL, "")
	_ = eng.SetConfig(Config{TargetInterface: "nwg0"})

	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	go eng.Run(ctx)

	// ждём, пока наблюдатели сработают
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		rows := eng.Table().List()
		if len(rows) >= 1 {
			break
		}
		time.Sleep(50 * time.Millisecond)
	}
	rows := eng.Table().List()
	if len(rows) < 1 {
		t.Fatalf("кандидаты не появились: %+v", rows)
	}

	// Домен из dial-логов — кандидат.
	foundDead := false
	for _, r := range rows {
		if r.Domain == "dead-site.example" {
			foundDead = true
		}
	}
	if !foundDead {
		t.Fatalf("dead-site.example не в кандидатах: %+v", rows)
	}

	// Соединение, уже идущее через туннель, кандидатом быть не может:
	// pushTunneledStallConn пишет outbound vpn-group.
	before := len(eng.Table().List())
	for i := 0; i < 6; i++ {
		awgm.pushTunneledStallConn("already-vpn.example", "203.0.113.90", "443", 20000, 128)
	}
	eng.obs.PollConnections(context.Background())
	if len(eng.Table().List()) != before {
		t.Fatalf("туннелированное соединение попало в кандидаты")
	}

	// Правила пока не добавлялись (нет подтверждённых блокировок пробы):
	// авто-добавление у нас срабатывает только после blocked-пробы, а пробы
	// dead-site.example идут на несуществующий домен — вердикт unknown.
	if len(awgm.addedRules()) != 0 {
		t.Fatalf("без пробы-подтверждения правил быть не должно: %v", awgm.addedRules())
	}
}

func (f *fakeAWGM) pushTunneledStallConn(host, ip, port string, up, down int64) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.conns = append(f.conns, map[string]any{
		"metadata": map[string]any{
			"network": "tcp", "sourceIP": "192.168.31.10",
			"destinationIP": ip, "destinationPort": port, "host": host,
		},
		"upload": up, "download": down,
		"start":    time.Now().Add(-30 * time.Second).UTC().Format(time.RFC3339),
		"outbound": "vpn-group",
		"chains":   []string{"node1", "vpn-group"},
	})
}

func (f *fakeAWGM) addedRules() []string {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]string(nil), f.added...)
}

// TestCoverageFilter — домен, уже покрытый правилом роутера (list/geosite),
// и домен, чей резолв покрыт geoip/ip_cidr, кандидатами не становятся.
func TestCoverageFilter(t *testing.T) {
	awgm := newFakeAWGM(t)
	awgm.routedDomains["covered.example"] = true // доменное покрытие (list)
	awgm.routedDomains["wave.loop.ru"] = true    // правило на исходный хост, не на root
	awgm.routedDomains["203.0.113.99"] = true    // IP-покрытие (geoip)
	awgm.resolveByDomain["geoip-covered.example"] = []string{"203.0.113.99"}
	awgm.resolveByDomain["fresh.example"] = []string{"203.0.113.42"} // непокрытый IP

	eng := NewEngine(awgm.srv.URL, "")

	// домен, покрытый list/geosite — отклонён
	if eng.noteIfUnrouted(context.Background(), Host{Name: "www.covered.example"}, WhyNoAnswer, "dial") {
		t.Fatal("покрытый домен не должен номинироваться")
	}
	if eng.noteIfUnrouted(context.Background(), Host{Name: "wave.loop.ru"}, WhyFreeze, "stall") {
		t.Fatal("правило на исходный поддомен должно быть найдено до проверки root")
	}
	// домен, чей резолв попал в geoip — отклонён
	if eng.noteIfUnrouted(context.Background(), Host{Name: "geoip-covered.example"}, WhyNoAnswer, "dial") {
		t.Fatal("домен с покрытым резолвом не должен номинироваться")
	}
	// непокрытый — номинируется
	if !eng.noteIfUnrouted(context.Background(), Host{Name: "fresh.example"}, WhyNoAnswer, "dial") {
		t.Fatal("непокрытый домен должен номинироваться")
	}
}

func TestProbeQueueSingleFlight(t *testing.T) {
	eng := NewEngine("http://127.0.0.1:1", "")
	eng.enqueue(probeTask{host: "a.example", domain: "a.example"})
	eng.enqueue(probeTask{host: "b.example", domain: "b.example"})
	first, ok := eng.nextProbe()
	if !ok || first.host == "" {
		t.Fatal("первая проба должна быть выдана")
	}
	if _, ok := eng.nextProbe(); ok {
		t.Fatal("autolist app не запускает вторую пробу, пока первая активна")
	}
	eng.completeProbe(first, ProbeResult{Verdict: VerdictUnknown}, time.Now())
	if _, ok := eng.nextProbe(); !ok {
		t.Fatal("после завершения должна быть выдана следующая проба")
	}
}

func TestProbePriorityAndMetadata(t *testing.T) {
	eng := NewEngine("http://127.0.0.1:1", "")
	now := time.Unix(1000, 0)
	eng.now = func() time.Time { return now }
	note := probeTask{
		host: "www.note.example", domain: "note.example", ip: "203.0.113.10",
		why: WhyFreeze, source: "open", probeOnly: true,
	}
	if !eng.enqueue(note) {
		t.Fatal("номинация не поставлена")
	}
	if ask := eng.RequestProbe("User.Example."); ask != probeq.AskQueued {
		t.Fatalf("manual ask = %v, want queued", ask)
	}
	first, ok := eng.nextProbe(false)
	if !ok || first.host != "user.example" {
		t.Fatalf("первой должна идти USER-проба: %+v, ok=%v", first, ok)
	}
	eng.completeProbe(first, ProbeResult{Verdict: VerdictUnknown}, now)
	if _, ok := eng.nextProbe(false); ok {
		t.Fatal("USER wake не должен выкачивать watcher NOTE")
	}
	third, ok := eng.nextProbe(true)
	if !ok || third.host != note.host || third.domain != note.domain || third.ip != note.ip ||
		third.why != note.why || third.source != note.source || !third.probeOnly {
		t.Fatalf("NOTE или её метаданные потеряны: %+v, ok=%v", third, ok)
	}
}

func TestProbeCandidateLifecycle(t *testing.T) {
	t.Run("unknown remains visible and never suppresses a fresh probe", func(t *testing.T) {
		eng := NewEngine("http://127.0.0.1:1", "")
		now := time.Unix(1000, 0)
		eng.now = func() time.Time { return now }
		task := probeTask{host: "unknown.example", domain: "unknown.example", source: "stall"}
		eng.table.Note(Host{Name: task.host}, WhyNoAnswer, "stall", now)
		if !eng.enqueue(task) {
			t.Fatal("проба не поставлена")
		}
		queued, _ := eng.nextProbe()
		eng.completeProbe(queued, ProbeResult{Verdict: VerdictUnknown, Err: "temporary"}, now)
		rows := eng.table.List()
		if len(rows) != 1 || rows[0].Verdict != VerdictUnknown.String() {
			t.Fatalf("unknown lifecycle = %+v", rows)
		}
		if !eng.enqueue(task) {
			t.Fatal("старый unknown запретил новую проверку")
		}
		if _, ok := eng.nextProbe(); !ok {
			t.Fatal("новая проверка после unknown не выдана")
		}
	})

	t.Run("reachable ends only current event without dismiss or history", func(t *testing.T) {
		eng := NewEngine("http://127.0.0.1:1", "")
		now := time.Unix(2000, 0)
		eng.now = func() time.Time { return now }
		task := probeTask{host: "reachable.example", domain: "reachable.example", source: "stall"}
		eng.table.Note(Host{Name: task.host}, WhyFreeze, "stall", now)
		eng.enqueue(task)
		queued, _ := eng.nextProbe()
		eng.completeProbe(queued, ProbeResult{Verdict: VerdictReachable}, now)
		if rows := eng.table.List(); len(rows) != 0 {
			t.Fatalf("reachable должен завершить кандидата: %+v", rows)
		}
		if history := snapshotHistory(t, eng); len(history) != 0 {
			t.Fatalf("reachable создал историю: %+v", history)
		}
		if !eng.table.Note(Host{Name: task.host}, WhyFreeze, "stall", now.Add(time.Second)) {
			t.Fatal("reachable автоматически скрыл следующее событие")
		}
	})

	t.Run("blocked stores diagnostics and permits a fresh probe", func(t *testing.T) {
		eng := NewEngine("http://127.0.0.1:1", "")
		now := time.Unix(3000, 0)
		eng.now = func() time.Time { return now }
		task := probeTask{host: "blocked.example", domain: "blocked.example", source: "open", probeOnly: true}
		eng.enqueue(task)
		queued, _ := eng.nextProbe()
		eng.completeProbe(queued, ProbeResult{Verdict: VerdictBlocked, Why: WhyReset, Bytes: 123, Err: "reset"}, now)
		rows := eng.table.List()
		if len(rows) != 1 || rows[0].Verdict != VerdictBlocked.String() {
			t.Fatalf("blocked lifecycle = %+v", rows)
		}
		if !eng.enqueue(task) {
			t.Fatal("blocked подавил следующую проверку")
		}
	})
}

func TestNewObservationAlwaysStartsFreshProbe(t *testing.T) {
	eng := NewEngine("http://127.0.0.1:1", "")
	now := time.Unix(6000, 0)
	eng.now = func() time.Time { return now }
	host := "static.zulipchat.com"
	for _, source := range []string{"open", "stall"} {
		task := probeTask{host: host, domain: "zulipchat.com", source: source, probeOnly: source == "open"}
		if !eng.enqueue(task) {
			t.Fatalf("новое наблюдение %s подавлено старым unknown", source)
		}
		queued, ok := eng.nextProbe()
		if !ok || queued.host != host {
			t.Fatalf("fresh %s probe = %+v, ok=%v", source, queued, ok)
		}
		eng.completeProbe(queued, ProbeResult{Verdict: VerdictUnknown, Err: "DNS unavailable"}, now)
	}
}

func TestProbeHourlyLimitPolicy(t *testing.T) {
	eng := NewEngine("http://127.0.0.1:1", "")
	now := time.Unix(4000, 0)
	eng.now = func() time.Time { return now }
	starts := probeq.StartsPerHour
	if starts == 0 {
		starts = 31
	}
	for i := 0; i < starts; i++ {
		host := fmt.Sprintf("h%d.example", i)
		eng.enqueue(probeTask{host: host, domain: host, probeOnly: true})
		task, ok := eng.nextProbe()
		if !ok {
			t.Fatalf("проба %d не выдана до исчерпания бюджета", i)
		}
		eng.completeProbe(task, ProbeResult{Verdict: VerdictUnknown}, now)
	}
	eng.enqueue(probeTask{host: "waiting.example", domain: "waiting.example", probeOnly: true})
	if probeq.StartsPerHour == 0 {
		task, ok := eng.nextProbe()
		if !ok || task.host != "waiting.example" {
			t.Fatalf("проба заблокирована при StartsPerHour=0: %+v, ok=%v", task, ok)
		}
		eng.completeProbe(task, ProbeResult{Verdict: VerdictUnknown}, now)
		snapshot := eng.Snapshot()
		if snapshot["probesUsedHour"] != 0 || snapshot["probesQueued"] != 0 || snapshot["probesInFlight"] != 0 {
			t.Fatalf("snapshot без часового лимита = %+v", snapshot)
		}
		return
	}
	if _, ok := eng.nextProbe(); ok {
		t.Fatal("проба запущена после исчерпания часового бюджета")
	}
	snapshot := eng.Snapshot()
	if snapshot["probesUsedHour"] != probeq.StartsPerHour || snapshot["probesQueued"] != 1 || snapshot["probesInFlight"] != 0 {
		t.Fatalf("snapshot budget = %+v", snapshot)
	}
	now = now.Add(time.Hour)
	if task, ok := eng.nextProbe(); !ok || task.host != "waiting.example" {
		t.Fatalf("ожидающая проба не выдана после истечения окна: %+v, ok=%v", task, ok)
	}
}

func TestProbeResultUsesRegisteredDomainKey(t *testing.T) {
	awgm := newFakeAWGM(t)
	eng := NewEngine(awgm.srv.URL, "")
	eng.prober.CurlPath = fakeCurl(t, "printf ok")
	eng.table.Note(Host{Name: "beta.spruthub.ru"}, WhyFreeze, "stall", time.Now())
	eng.probeOne(probeTask{host: "beta.spruthub.ru", domain: "spruthub.ru"})
	if rows := eng.table.List(); len(rows) != 0 {
		t.Fatalf("reachable-проба должна удалить строку spruthub.ru, а не beta.spruthub.ru: %+v", rows)
	}
}

func TestProbeOnlyCreatesCandidateOnlyWhenBlocked(t *testing.T) {
	awgm := newFakeAWGM(t)
	eng := NewEngine(awgm.srv.URL, "")
	task := probeTask{host: "pocket-id.org", domain: "pocket-id.org", why: WhyFreeze, source: "open", probeOnly: true}

	eng.prober.CurlPath = fakeCurl(t, "printf ok")
	eng.probeOne(task)
	if rows := eng.table.List(); len(rows) != 0 {
		t.Fatalf("reachable probe-only не должен создавать кандидата: %+v", rows)
	}

	// reachable выше намеренно скрыл домен на сутки; используем новый Engine,
	// чтобы проверить blocked без влияния этого кэша.
	eng = NewEngine(awgm.srv.URL, "")
	eng.prober.CurlPath = fakeCurl(t, "printf 'read: connection reset by peer\\n' >&2\nexit 35")
	eng.probeOne(task)
	rows := eng.table.List()
	if len(rows) != 1 || rows[0].Domain != "pocket-id.org" || rows[0].Verdict != VerdictBlocked.String() {
		t.Fatalf("blocked probe-only должен создать подтверждённого кандидата: %+v", rows)
	}
}

func TestOpenSuspicionStaysOutOfTableUntilProbe(t *testing.T) {
	awgm := newFakeAWGM(t)
	eng := NewEngine(awgm.srv.URL, "")
	now := time.Now()
	eng.obs.now = func() time.Time { return now }

	autolistConn(awgm, "T1", "pocket-id.org", 10000, 60*time.Second)
	eng.tick(context.Background())
	now = now.Add(15 * time.Second)
	awgm.resetConns()
	autolistConn(awgm, "T1", "pocket-id.org", 10000, 60*time.Second)
	eng.tick(context.Background())
	if len(eng.table.List()) != 0 {
		t.Fatal("probe-only подозрение не должно создавать строку до пробы")
	}
	task, ok := eng.nextProbe(true)
	if !ok || !task.probeOnly || task.domain != "pocket-id.org" {
		t.Fatalf("ожидалась одна probe-only задача: %+v, ok=%v", task, ok)
	}
}

func TestCandidateProbePumpsImmediatelyAndCoalescesRepeats(t *testing.T) {
	awgm := newFakeAWGM(t)
	eng := NewEngine(awgm.srv.URL, "")
	probed := make(chan string, 4)
	release := make(chan struct{})
	eng.prober.CurlPath = fakeCurl(t, "printf 'temporary\\n' >&2\nexit 1")
	eng.prober.OnStart = func(host string) {
		probed <- host
		<-release
	}
	now := time.Now()
	eng.table.Note(Host{Name: "www.candidate.example"}, WhyNoAnswer, "dial", now)
	task := probeTask{host: "www.candidate.example", domain: "candidate.example", why: WhyNoAnswer, source: "dial"}
	if !eng.enqueue(task) || !eng.enqueue(task) {
		t.Fatal("new candidate nomination was not accepted and coalesced")
	}

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go eng.Run(ctx)
	select {
	case host := <-probed:
		if host != task.host {
			t.Fatalf("immediate candidate probe host = %s, want %s", host, task.host)
		}
	case <-time.After(time.Second):
		t.Fatal("new candidate probe did not start before the 120-second tick")
	}
	// In-flight nominations also return AskQueued; they must not create a
	// second queue entry or another probe after this one completes.
	if !eng.enqueue(task) || !eng.enqueue(task) {
		t.Fatal("in-flight repeat was not coalesced")
	}
	close(release)
	deadline := time.Now().Add(time.Second)
	for {
		row, exists := eng.table.get(task.domain)
		if exists && row.Verdict == VerdictUnknown.String() {
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("first candidate probe did not complete")
		}
		time.Sleep(5 * time.Millisecond)
	}
	select {
	case host := <-probed:
		t.Fatalf("repeated nominations started a duplicate probe for %s", host)
	case <-time.After(100 * time.Millisecond):
	}
	wantUsed := 1
	if probeq.StartsPerHour == 0 {
		wantUsed = 0
	}
	if used := eng.Snapshot()["probesUsedHour"]; used != wantUsed {
		t.Fatalf("candidate probes used = %v, want %d", used, wantUsed)
	}

	cancel()
	select {
	case <-eng.done:
	case <-time.After(time.Second):
		t.Fatal("Engine не остановился")
	}
}

func TestCandidateProbeBacklogDrainsAfterCoalescedWake(t *testing.T) {
	awgm := newFakeAWGM(t)
	eng := NewEngine(awgm.srv.URL, "")
	probed := make(chan string, 4)
	eng.prober.CurlPath = fakeCurl(t, "printf 'temporary\\n' >&2\nexit 1")
	eng.prober.OnStart = func(host string) { probed <- host }
	for _, host := range []string{"a.example", "b.example"} {
		eng.table.Note(Host{Name: host}, WhyNoAnswer, "dial", time.Now())
		if !eng.enqueue(probeTask{host: host, domain: host, why: WhyNoAnswer, source: "dial"}) {
			t.Fatalf("candidate %s was not queued", host)
		}
	}

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go eng.Run(ctx)
	seen := map[string]bool{}
	for len(seen) < 2 {
		select {
		case host := <-probed:
			if seen[host] {
				t.Fatalf("duplicate backlog probe for %s", host)
			}
			seen[host] = true
		case <-time.After(time.Second):
			t.Fatalf("coalesced candidate backlog did not drain; started=%v", seen)
		}
	}
	wantUsed := 2
	if probeq.StartsPerHour == 0 {
		wantUsed = 0
	}
	if used := eng.Snapshot()["probesUsedHour"]; used != wantUsed {
		t.Fatalf("backlog probes used = %v, want %d", used, wantUsed)
	}
	cancel()
	select {
	case <-eng.done:
	case <-time.After(time.Second):
		t.Fatal("Engine не остановился")
	}
}

func TestManualProbePumpsImmediatelyButNominationWaits(t *testing.T) {
	awgm := newFakeAWGM(t)
	eng := NewEngine(awgm.srv.URL, "")
	probed := make(chan string, 4)
	eng.prober.CurlPath = fakeCurl(t, "printf ok")
	eng.prober.OnStart = func(host string) { probed <- host }
	eng.enqueue(probeTask{host: "note.example", domain: "note.example", probeOnly: true})

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go eng.Run(ctx)
	select {
	case host := <-probed:
		t.Fatalf("NOTE запустилась без 120-секундного тика: %s", host)
	case <-time.After(75 * time.Millisecond):
	}
	if ask := eng.RequestProbe("manual.example"); ask != probeq.AskQueued {
		t.Fatalf("manual ask = %v", ask)
	}
	select {
	case host := <-probed:
		if host != "manual.example" {
			t.Fatalf("немедленно запущена %s вместо manual.example", host)
		}
	case <-time.After(time.Second):
		t.Fatal("USER-проба не запустилась немедленно")
	}
	select {
	case host := <-probed:
		t.Fatalf("USER wake не должен выкачивать NOTE: %s", host)
	case <-time.After(75 * time.Millisecond):
	}
	cancel()
	select {
	case <-eng.done:
	case <-time.After(time.Second):
		t.Fatal("Engine не остановился")
	}
}

func TestReachableNeverRemovesExistingRule(t *testing.T) {
	awgm := newFakeAWGM(t)
	awgm.rulesets = []map[string]any{{
		"tag": ruleSetTag, "type": "inline",
		"rules": []map[string]any{{"domain_suffix": []any{"keep.example"}}},
	}}
	awgm.added = []string{"keep.example"}
	eng := NewEngine(awgm.srv.URL, "")
	eng.completeProbe(probeTask{host: "keep.example", domain: "keep.example"}, ProbeResult{
		Verdict: VerdictReachable, ShortBody: true, Bytes: 2,
	}, time.Now())
	if got := awgm.addedRules(); len(got) != 1 || got[0] != "keep.example" {
		t.Fatalf("direct reachable удалил существующее правило: %+v", got)
	}
	if awgm.applied != 0 {
		t.Fatal("reachable вызвал AutoOff/apply")
	}
}

// TestConfigPersistence — конфиг переживает пересоздание движка.
func TestConfigPersistence(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "cfg.json")

	eng := NewEngine("http://127.0.0.1:1", "")
	if err := eng.LoadConfig(path); err != nil {
		t.Fatal(err)
	}
	if err := eng.SetConfig(Config{AutoAdd: true, TunnelProbe: true, TargetInterface: "nwg0", TargetOutbound: "vpn", ResolveVia: "9.9.9.9:53"}); err != nil {
		t.Fatal(err)
	}

	eng2 := NewEngine("http://127.0.0.1:1", "")
	if err := eng2.LoadConfig(path); err != nil {
		t.Fatal(err)
	}
	cfg := eng2.Config()
	if !cfg.AutoAdd || !cfg.TunnelProbe || cfg.TargetInterface != "nwg0" || cfg.TargetOutbound != "vpn" || cfg.ResolveVia != "9.9.9.9:53" {
		t.Fatalf("конфиг не пережил перезагрузку: %+v", cfg)
	}
	// умный дефолт ResolveIPEnabled
	if !cfg.ResolveIPOn() {
		t.Fatal("ResolveIPOn по умолчанию должен быть true")
	}
}

// TestBatchedRuleSetUpdate — несколько доменов за один вызов дают ОДИН
// update набора и ОДИН staging/apply (одна перезагрузка движка).
func TestBatchedRuleSetUpdate(t *testing.T) {
	awgm := newFakeAWGM(t)
	eng := NewEngine(awgm.srv.URL, "")
	// тестовый outbound: иначе берётся «последнее не-direct правило» роутера,
	// которого фейк не содержит
	_ = eng.SetConfig(Config{TargetInterface: "nwg0"})

	res := eng.AddToTunnel([]string{"a.example", "b.example", "c.example"})
	for d, err := range res {
		if err != nil {
			t.Fatalf("%s: %v", d, err)
		}
	}
	ops := awgm.rulesetOps
	updates := 0
	for _, op := range ops {
		if len(op) > 7 && op[:7] == "update:" {
			updates++
		}
	}
	if updates != 0 { // первый вызов создаёт набор — добавление без update
		t.Fatalf("ожидался 1 add без update, ops=%v", ops)
	}
	if len(awgm.added) != 3 {
		t.Fatalf("в наборе %d доменов, want 3: %v", len(awgm.added), awgm.added)
	}
	if awgm.applied != 1 {
		t.Fatalf("staging/apply вызван %d раз, want 1", awgm.applied)
	}

	// второй вызов — уже update существующего набора, тоже один
	res = eng.AddToTunnel([]string{"d.example"})
	if res["d.example"] != nil {
		t.Fatalf("d.example: %v", res["d.example"])
	}
	updates = 0
	for _, op := range awgm.rulesetOps {
		if len(op) > 7 && op[:7] == "update:" {
			updates++
		}
	}
	if updates != 1 || len(awgm.added) != 4 || awgm.applied != 2 {
		t.Fatalf("второй вызов: updates=%d added=%v applied=%d", updates, awgm.added, awgm.applied)
	}
}

func TestAdditionHourlyLimitIsFifty(t *testing.T) {
	awgm := newFakeAWGM(t)
	eng := NewEngine(awgm.srv.URL, "")
	if err := eng.SetConfig(Config{TargetInterface: "nwg0"}); err != nil {
		t.Fatal(err)
	}
	domains := make([]string, AddsPerHour+1)
	for i := range domains {
		domains[i] = fmt.Sprintf("limit-%d.example", i)
	}
	results := eng.AddToTunnel(domains)
	for _, domain := range domains[:AddsPerHour] {
		if err := results[domain]; err != nil {
			t.Fatalf("%s rejected before the limit: %v", domain, err)
		}
	}
	if err := results[domains[AddsPerHour]]; err == nil {
		t.Fatal("51st addition did not reach the hourly limit")
	}
	if got := len(awgm.addedRules()); got != AddsPerHour {
		t.Fatalf("added domains = %d, want %d", got, AddsPerHour)
	}
	snapshot := eng.Snapshot()
	if snapshot["addsUsedHour"] != AddsPerHour || snapshot["addsLimit"] != 50 {
		t.Fatalf("addition limit status = used %v, limit %v", snapshot["addsUsedHour"], snapshot["addsLimit"])
	}
}

func TestInlineRuleSetNoDraftIsSuccess(t *testing.T) {
	awgm := newFakeAWGM(t)
	awgm.applyNoDraft = true
	awgm.rulesets = []map[string]any{{
		"tag": ruleSetTag, "type": "inline",
		"rules": []map[string]any{{"domain_suffix": []any{"old.example"}}},
	}}
	awgm.rules = []map[string]any{{
		"rule_set": []string{ruleSetTag}, "action": "route", "outbound": autolistTargetTag,
	}}
	eng := NewEngine(awgm.srv.URL, "")
	if err := eng.rules.AddDomainSuffixRule(context.Background(), []string{"new.example"}, "nwg0"); err != nil {
		t.Fatalf("hot-reload inline набора без draft не должен быть ошибкой: %v", err)
	}
	if len(awgm.rules) != 1 {
		t.Fatalf("owner-правило задублировалось: %v", awgm.rules)
	}
	if got := awgm.addedRules(); len(got) != 2 || got[0] != "new.example" || got[1] != "old.example" {
		t.Fatalf("набор не обновлён: %v", got)
	}
}

// TestAutoAddAfterConfirm — авто-добавление после подтверждённой блокировки.
func TestAutoAddAfterConfirm(t *testing.T) {
	awgm := newFakeAWGM(t)
	eng := NewEngine(awgm.srv.URL, "")
	_ = eng.SetConfig(Config{TargetInterface: "nwg0"})

	// кандидат
	h := Host{Name: "chilled.example.org", IP: "203.0.113.5"}
	if !eng.Table().Note(h, WhyNoAnswer, "dial", time.Now()) {
		t.Fatal("кандидат не создан")
	}
	// проба подтверждает блокировку
	eng.table.Confirm("chilled.example.org", VerdictBlocked, time.Now())
	res := eng.AddToTunnel([]string{"chilled.example.org"})
	if res["chilled.example.org"] != nil {
		t.Fatalf("add failed: %v", res["chilled.example.org"])
	}

	got := awgm.addedRules()
	if len(got) != 1 || got[0] != "chilled.example.org" {
		t.Fatalf("added = %v, want [chilled.example.org]", got)
	}
	if awgm.applied == 0 {
		t.Fatal("staging/apply не вызван")
	}
	// Повторное добавление собственного домена — успешный no-op, даже когда
	// inspector уже видит его как маршрутизируемый нашим правилом.
	awgm.routedDomains["chilled.example.org"] = true
	res = eng.AddToTunnel([]string{"chilled.example.org"})
	if res["chilled.example.org"] != nil {
		t.Fatalf("повторное добавление должно быть успешным no-op: %v", res["chilled.example.org"])
	}
	if len(awgm.addedRules()) != 1 {
		t.Fatalf("дубли правил: %v", awgm.addedRules())
	}
}
