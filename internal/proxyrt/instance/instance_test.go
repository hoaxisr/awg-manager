package instance

import (
	"context"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/hoaxisr/awg-manager/internal/proxyrt"
	"github.com/hoaxisr/awg-manager/internal/proxyrt/roles"
)

// recordRole — роль, отдающая пустую декларацию и запоминающая конфиги,
// с которыми её звали.
type recordRole struct {
	mu   sync.Mutex
	cfgs []any
}

func (r *recordRole) Resources(_ proxyrt.Intent, cfg any, _ proxyrt.Observations) []proxyrt.Resource {
	r.mu.Lock()
	r.cfgs = append(r.cfgs, cfg)
	r.mu.Unlock()
	return nil
}

// snapshot — копия запомненного под мьютексом: пишет горутина воркера, читает
// горутина теста.
func (r *recordRole) snapshot() []any {
	r.mu.Lock()
	defer r.mu.Unlock()
	return append([]any(nil), r.cfgs...)
}

type memJournal struct {
	mu    sync.Mutex
	lines []string
}

func (m *memJournal) Info(action, target, message string) {
	m.mu.Lock()
	m.lines = append(m.lines, "I:"+action+":"+message)
	m.mu.Unlock()
}

func (m *memJournal) Warn(action, target, message string) {
	m.mu.Lock()
	m.lines = append(m.lines, "W:"+action+":"+message)
	m.mu.Unlock()
}

func (m *memJournal) count() int {
	m.mu.Lock()
	defer m.mu.Unlock()
	return len(m.lines)
}

func contextWithCancel() (context.Context, context.CancelFunc) {
	return context.WithCancel(context.Background())
}

// waitFor — поллинг условия со сроком: тест, который «висит», обязан упасть сам.
func waitFor(t *testing.T, cond func() bool) {
	t.Helper()
	deadline := time.Now().Add(2 * time.Second)
	for {
		if cond() {
			return
		}
		if time.Now().After(deadline) {
			t.Fatal("условие не наступило за 2 с")
		}
		time.Sleep(5 * time.Millisecond)
	}
}

func TestCfgReadPerRunNotAtConstruction(t *testing.T) {
	// Конфиг живёт у писателя (план 5) и меняется между прогонами; движковый
	// Reconciler держит cfg константой — обёртка обязана перечитывать.
	// current — под мьютексом (M-1 ревью-2): воркер читает Cfg() из своей
	// горутины (движок зовёт Resources дважды за проход), и голая запись из
	// теста честно валит -race.
	role := &recordRole{}
	var mu sync.Mutex
	current := "v1"
	setCfg := func(v string) { mu.Lock(); current = v; mu.Unlock() }
	getCfg := func() any { mu.Lock(); defer mu.Unlock(); return current }
	j := &memJournal{}
	inst := New(Config{
		ID: "i1", Role: role,
		Cfg:     getCfg,
		Intent:  func() proxyrt.Intent { return proxyrt.IntentEnabled },
		States:  proxyrt.NewStateStore(nil, nil),
		Journal: j,
	})
	ctx, cancel := contextWithCancel()
	defer cancel()
	inst.Start(ctx)
	inst.Post(proxyrt.EventBoot)
	waitFor(t, func() bool { return len(role.snapshot()) >= 1 })

	setCfg("v2")
	inst.Post(proxyrt.EventIntentChanged)
	waitFor(t, func() bool {
		cfgs := role.snapshot()
		return len(cfgs) > 0 && cfgs[len(cfgs)-1] == "v2"
	})
	inst.Stop()
}

func TestOneJournalLinePerReconcile(t *testing.T) {
	role := &recordRole{}
	j := &memJournal{}
	inst := New(Config{ID: "i1", Role: role,
		Cfg:     func() any { return nil },
		Intent:  func() proxyrt.Intent { return proxyrt.IntentEnabled },
		States:  proxyrt.NewStateStore(nil, nil),
		Journal: j,
	})
	ctx, cancel := contextWithCancel()
	defer cancel()
	inst.Start(ctx)
	inst.Post(proxyrt.EventBoot)
	waitFor(t, func() bool { return j.count() >= 1 })
	inst.Stop()
	if j.count() > 2 { // boot мог схлопнуться с дренажом стопа — но не больше
		t.Fatalf("журнал шумит: %v", j.lines)
	}
}

func TestSummarize(t *testing.T) {
	res := proxyrt.Result{
		Steps: []proxyrt.Step{{Resource: "ndms_address", Op: "set-address", Reason: "адрес не тот"}},
		States: []proxyrt.ResourceState{
			{ID: "process", Status: proxyrt.StatusOK},
			{ID: "ndms_address", Status: proxyrt.StatusFailed, Error: "rci отказал"},
			{ID: "ndms_admin_state", Status: proxyrt.StatusBlocked},
		},
		Passes: 2,
	}
	line := Summarize(res, proxyrt.PhaseFailed)
	for _, want := range []string{"failed", "ndms_address", "rci отказал", "проход"} {
		if !strings.Contains(line, want) {
			t.Fatalf("в сводке нет %q: %s", want, line)
		}
	}
	if strings.Count(line, "\n") != 0 {
		t.Fatal("сводка — одна строка")
	}
}

func TestSweepLabelsAndDeclaredNames(t *testing.T) {
	labels := SweepLabels()
	if len(labels) != 3 {
		t.Fatalf("меток три (сервер WG, сервер raw, клиент-префикс): %v", labels)
	}
	names := DeclaredNDMSNames([]NDMSNamed{
		roles.WdttServerConfig{NdmsIface: "OpkgTun17", RawNdmsIface: "OpkgTun19"},
		// ВЫКЛЮЧЕННЫЙ клиент тоже объявляет свои имена: sweep не сносит
		// ресурсы disabled-инстансов (спека §4.2).
		roles.WdttClientConfig{Mode: "raw", NdmsIface: "OpkgTun18"},
		roles.FreeTurnClientConfig{}, // NDMS-имён не имеет
	})
	for _, want := range []string{"OpkgTun17", "OpkgTun19", "OpkgTun18"} {
		if !names[want] {
			t.Fatalf("ведомость потеряла %s: %v", want, names)
		}
	}
	if len(names) != 3 {
		t.Fatalf("лишние имена: %v", names)
	}
}

// Все четыре конфига ролей обязаны объявлять свои NDMS-имена САМИ. Тип, который
// метод не объявил, в ведомость не попадёт — но и не соберётся: это и есть
// защита от «нового конфига, о котором ведомость не знает». Пример отказа
// сборки:
//
//	type новыйКонфиг struct{}
//	DeclaredNDMSNames([]NDMSNamed{новыйКонфиг{}})
//	// cannot use новыйКонфиг{} … as NDMSNamed value … missing method NDMSNames
var (
	_ NDMSNamed = roles.WdttClientConfig{}
	_ NDMSNamed = roles.WdttServerConfig{}
	_ NDMSNamed = roles.FreeTurnClientConfig{}
	_ NDMSNamed = roles.FreeTurnServerConfig{}
)

func TestDeclaredNDMSNamesPointerConfigSameAsValue(t *testing.T) {
	// Указатель на конфиг обязан дать ТЕ ЖЕ имена, что значение: старая
	// ведомость на []any молча роняла указатель, и sweeper сносил живой
	// интерфейс. Методы на значении — метод-сет *T их включает, поэтому
	// расхождение здесь означало бы, что кто-то завёл метод на указателе.
	val := roles.WdttServerConfig{NdmsIface: "OpkgTun17", RawNdmsIface: "OpkgTun19"}
	byValue := DeclaredNDMSNames([]NDMSNamed{val})
	byPointer := DeclaredNDMSNames([]NDMSNamed{&val})
	if len(byPointer) != len(byValue) {
		t.Fatalf("указатель дал другую ведомость: %v против %v", byPointer, byValue)
	}
	for name := range byValue {
		if !byPointer[name] {
			t.Fatalf("указатель потерял %s: %v", name, byPointer)
		}
	}
	cli := roles.WdttClientConfig{Mode: "raw", NdmsIface: "OpkgTun18"}
	if names := DeclaredNDMSNames([]NDMSNamed{&cli}); !names["OpkgTun18"] {
		t.Fatalf("указатель на клиента потерял имя: %v", names)
	}
}
