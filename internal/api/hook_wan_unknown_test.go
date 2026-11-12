package api

import (
	"strings"
	"sync"
	"testing"

	"github.com/hoaxisr/awg-manager/internal/logging"
)

type recordingAppLogger struct {
	mu   sync.Mutex
	msgs []string
}

func (l *recordingAppLogger) AppLog(_ logging.Level, _, _, action, target, message string) {
	l.mu.Lock()
	defer l.mu.Unlock()
	l.msgs = append(l.msgs, action+" "+target+" "+message)
}

func (l *recordingAppLogger) joined() string {
	l.mu.Lock()
	defer l.mu.Unlock()
	return strings.Join(l.msgs, "\n")
}

// #1166 (F785): хук публичного интерфейса на имя ядра, которого модель WAN не
// знает и после перечитывания, обязан оставить след в журнале — иначе канал
// молча выпадает из авто-выбора. Хук LAN-моста того же вида — рядовой, без записи.
func TestHookHandler_WANHookForUnknownName_IsLogged(t *testing.T) {
	for _, tc := range []struct {
		name    string
		model   HookWANModel
		public  bool
		wantLog bool
	}{
		{"public interface", &fakeWANModel{}, true, true},
		{"lan bridge", &fakeWANModel{}, false, false},
		{"known wan", &knownWANModel{}, true, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			rec := &recordingAppLogger{}
			h := newTestHookHandler(&spyDispatcher{})
			h.wanLog = logging.NewScopedLogger(rec, logging.GroupSystem, logging.SubWan)
			h.SetWANModel(tc.model)
			h.SetWANPublicCheck(func(string) bool { return tc.public })

			handleForm(t, h, "type=iflayerchanged&id=PPPoE0&system_name=ppp1&layer=ipv4&level=running")

			got := strings.Contains(rec.joined(), "missing from the WAN model")
			if got != tc.wantLog {
				t.Fatalf("запись о потерянном WAN: got %v, want %v; журнал:\n%s", got, tc.wantLog, rec.joined())
			}
		})
	}
}
