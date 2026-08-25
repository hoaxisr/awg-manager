package captcha

import (
	"reflect"
	"testing"

	"github.com/hoaxisr/awg-manager/awgmproto"
	"github.com/hoaxisr/awg-manager/internal/proxyrt/instancestore"
	"github.com/hoaxisr/awg-manager/internal/proxyrt/roles"
)

// ── фикстуры и фейки ─────────────────────────────────────────────

const (
	keyDefault = "freeturn-client:default"
	keySecond  = "freeturn-client:second"

	waitingLog = "2026/07/22 10:43:48 [INFO] [STREAM 1] [Captcha] Triggering manual captcha fallback\n"
	calmLog    = "2026/07/22 10:43:48 [INFO] [STREAM 1] Established DTLS connection\n"
)

func ftClient(id, name string) instancestore.Record {
	return instancestore.Record{
		ID: id, Kind: instancestore.KindFreeTurnClient, Name: name, Enabled: true,
		FreeTurnClient: &roles.FreeTurnClientConfig{Listen: "127.0.0.1:9100", Peer: "1.2.3.4:5000"},
	}
}

// fakeRecords — и RecordSource, и RecordLister: за обоими в проде стоит один
// менеджер. Get отвечает ТОЛЬКО на свой ключ.
type fakeRecords struct{ recs []instancestore.Record }

func (f *fakeRecords) Records() []instancestore.Record { return f.recs }

func (f *fakeRecords) Get(key string) (instancestore.Record, bool) {
	for _, r := range f.recs {
		if r.Key() == key {
			return r, true
		}
	}
	return instancestore.Record{}, false
}

// fakeListener запоминает СОСТАВ кандидатов, с которым его позвали.
type fakeListener struct {
	owner int
	open  bool
	calls [][]int
}

func (f *fakeListener) fn(candidates []int) (int, bool) {
	f.calls = append(f.calls, append([]int(nil), candidates...))
	return f.owner, f.open
}

func snapshots(pids map[string]int) func(string) (awgmproto.State, bool) {
	return func(key string) (awgmproto.State, bool) {
		pid, ok := pids[key]
		if !ok {
			return awgmproto.State{}, false
		}
		return awgmproto.State{Instance: key, PID: pid}, true
	}
}

func logs(byKey map[string]string) func(string) string {
	return func(key string) string { return byKey[key] }
}

// ── тесты ────────────────────────────────────────────────────────

func TestStatus_URLUsesInstanceKey(t *testing.T) {
	lis := &fakeListener{owner: 41, open: true}
	s := New(Deps{
		Records:   &fakeRecords{recs: []instancestore.Record{ftClient("default", "Дом")}},
		Instances: &fakeRecords{recs: []instancestore.Record{ftClient("default", "Дом")}},
		Snapshots: snapshots(map[string]int{keyDefault: 41}),
		Log:       logs(map[string]string{keyDefault: waitingLog}),
		Listener:  lis.fn,
	})

	got := s.Status()

	want := Overview{
		PortOpen:      true,
		OwnerClientID: keyDefault,
		OwnerName:     "Дом",
		Clients: []ClientStatus{{
			ClientID:       keyDefault,
			ClientName:     "Дом",
			Waiting:        true,
			Active:         true,
			CanOpen:        true,
			URL:            "/api/proxyrt/instances/freeturn-client:default/captcha/",
			PendingStreams: 1,
			CaptchaSession: 1,
		}},
	}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("обзор:\n got %+v\nwant %+v", got, want)
	}
	if len(lis.calls) != 1 || !reflect.DeepEqual(lis.calls[0], []int{41}) {
		t.Fatalf("кандидаты владельца порта = %v, want [[41]]", lis.calls)
	}
}

func TestStatus_OnlyFreeTurnClients(t *testing.T) {
	recs := []instancestore.Record{
		{ID: "default", Kind: instancestore.KindWdttClient, Name: "wdtt клиент",
			WdttClient: &roles.WdttClientConfig{Listen: "127.0.0.1:9000"}},
		{ID: "default", Kind: instancestore.KindWdttServer, Name: "wdtt сервер",
			WdttServer: &roles.WdttServerConfig{Listen: "0.0.0.0:56000"}},
		{ID: "default", Kind: instancestore.KindFreeTurnServer, Name: "ft сервер",
			FreeTurnServer: &roles.FreeTurnServerConfig{Listen: "0.0.0.0:7000"}},
		ftClient("default", "ft клиент"),
	}
	lis := &fakeListener{open: true}
	s := New(Deps{
		Instances: &fakeRecords{recs: recs},
		// Снимки есть у ВСЕХ ролей: чужие pid не имеют права попасть в
		// кандидаты владельца порта капчи.
		Snapshots: snapshots(map[string]int{
			"wdtt-client:default": 11, "wdtt-server:default": 12,
			"freeturn-server:default": 13, keyDefault: 14,
		}),
		Listener: lis.fn,
	})

	got := s.Status()

	if len(got.Clients) != 1 || got.Clients[0].ClientID != keyDefault {
		t.Fatalf("в обзоре обязан быть только freeturn-клиент, got %+v", got.Clients)
	}
	if !reflect.DeepEqual(lis.calls[0], []int{14}) {
		t.Fatalf("кандидаты = %v, want [14] (только freeturn-клиент)", lis.calls[0])
	}
}

func TestStatus_QueueWhenTwoWaiting(t *testing.T) {
	recs := []instancestore.Record{ftClient("default", "Первый"), ftClient("second", "Второй")}
	lis := &fakeListener{owner: 41, open: true}
	s := New(Deps{
		Instances: &fakeRecords{recs: recs},
		Snapshots: snapshots(map[string]int{keyDefault: 41, keySecond: 42}),
		Log:       logs(map[string]string{keyDefault: waitingLog, keySecond: waitingLog}),
		Listener:  lis.fn,
	})

	got := s.Status()

	if got.OwnerClientID != keyDefault {
		t.Fatalf("владелец = %q, want %q", got.OwnerClientID, keyDefault)
	}
	first, second := got.Clients[0], got.Clients[1]
	if !first.Active || !first.CanOpen || first.URL != "/api/proxyrt/instances/freeturn-client:default/captcha/" {
		t.Fatalf("владелец порта: %+v", first)
	}
	if second.Active || second.CanOpen || second.URL != "" || !second.Queued {
		t.Fatalf("второй ожидающий обязан стоять в очереди без ссылки: %+v", second)
	}
	if !reflect.DeepEqual(lis.calls[0], []int{41, 42}) {
		t.Fatalf("кандидаты = %v, want [41 42]", lis.calls[0])
	}
}

func TestStatus_LogTakenPerKey(t *testing.T) {
	recs := []instancestore.Record{ftClient("default", "Первый"), ftClient("second", "Второй")}
	lis := &fakeListener{owner: 42, open: true}
	s := New(Deps{
		Instances: &fakeRecords{recs: recs},
		Snapshots: snapshots(map[string]int{keyDefault: 41, keySecond: 42}),
		// Ждёт ВТОРОЙ; если журнал берут не по ключу, признак уедет первому.
		Log:      logs(map[string]string{keyDefault: calmLog, keySecond: waitingLog}),
		Listener: lis.fn,
	})

	got := s.Status()

	if got.Clients[0].Waiting {
		t.Fatalf("первый инстанс не ждёт капчу: %+v", got.Clients[0])
	}
	if !got.Clients[1].Waiting || !got.Clients[1].CanOpen {
		t.Fatalf("второй инстанс ждёт капчу и владеет портом: %+v", got.Clients[1])
	}
	if got.OwnerClientID != keySecond {
		t.Fatalf("владелец = %q, want %q", got.OwnerClientID, keySecond)
	}
}

func TestStatus_NotRunningIsNotCandidate(t *testing.T) {
	recs := []instancestore.Record{ftClient("default", "Первый"), ftClient("second", "Второй")}
	lis := &fakeListener{open: true}
	s := New(Deps{
		Instances: &fakeRecords{recs: recs},
		// У первого снимка нет вовсе, у второго снимок без pid — оба не бегут.
		Snapshots: snapshots(map[string]int{keySecond: 0}),
		Log:       logs(map[string]string{keyDefault: waitingLog, keySecond: waitingLog}),
		Listener:  lis.fn,
	})

	got := s.Status()

	if len(lis.calls) != 1 || len(lis.calls[0]) != 0 {
		t.Fatalf("кандидаты = %v, want пусто", lis.calls)
	}
	for _, c := range got.Clients {
		if c.Waiting || c.Active || c.CanOpen {
			t.Fatalf("у неработающего инстанса нет ни ожидания, ни ссылки: %+v", c)
		}
	}
}

func TestStatusForKey(t *testing.T) {
	lis := &fakeListener{owner: 41, open: true}
	s := New(Deps{
		Instances: &fakeRecords{recs: []instancestore.Record{ftClient("default", "Дом")}},
		Snapshots: snapshots(map[string]int{keyDefault: 41}),
		Log:       logs(map[string]string{keyDefault: waitingLog}),
		Listener:  lis.fn,
	})

	got, ok := s.StatusForKey(keyDefault)
	if !ok || got.ClientID != keyDefault || got.URL != "/api/proxyrt/instances/freeturn-client:default/captcha/" {
		t.Fatalf("статус инстанса: %+v (ok=%v)", got, ok)
	}
	if _, ok := s.StatusForKey("freeturn-client:missing"); ok {
		t.Fatal("неизвестный ключ не имеет статуса")
	}
	// Ключ обзора — КЛЮЧ записи, а не её id: id `default` есть у всех ролей.
	if _, ok := s.StatusForKey("default"); ok {
		t.Fatal("статус обязан спрашиваться по ключу, а не по id")
	}
}
