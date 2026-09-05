package roletest

import (
	"context"
	"strings"
	"testing"

	"github.com/hoaxisr/awg-manager/internal/proxyrt"
)

// Converge доводит роль настоящим реконсилятором и ТРЕБУЕТ, чтобы цепочка
// сошлась.
//
// Обязательный ассерт фазы — главная защита этих тестов от ложного зелёного.
// Дыра в фикстуре (протухший снимок процесса, отсутствующий интерфейс,
// неотвечающий firewall) оставляет ресурс в waiting или blocked: до целевого
// шага дело не доходит, модель роутера остаётся пустой — и ассерты по пустой
// модели прошли бы молча. Поэтому недоведённая цепочка это провал теста здесь,
// с дампом состояний, а не загадочный зелёный ниже.
func Converge(t *testing.T, role proxyrt.Role, cfg any, intent proxyrt.Intent) proxyrt.Result {
	t.Helper()
	res, phase := proxyrt.NewReconciler(role, cfg, proxyrt.ReconcileOpts{MaxPasses: 8}).
		Run(context.Background(), intent)
	// Для снятого намерения доведённая цепочка даёт PhaseDisabled, а не
	// PhaseSettled (proxyrt/phase.go). Прежняя редакция писала сюда
	// PhaseSettled в обеих ветках — no-op, который уронил бы первый же
	// disabled-пин.
	want := proxyrt.PhaseSettled
	if intent == proxyrt.IntentDisabled {
		want = proxyrt.PhaseDisabled
	}
	if phase != want {
		t.Fatalf("цепочка не сошлась: фаза %q, ждали %q\n%s", phase, want, dumpStates(res))
	}
	return res
}

func dumpStates(res proxyrt.Result) string {
	var b strings.Builder
	b.WriteString("состояния ресурсов:\n")
	for _, st := range res.States {
		b.WriteString("  " + string(st.ID) + ": " + string(st.Status))
		if st.Detail != "" {
			b.WriteString(" — " + st.Detail)
		}
		if st.Error != "" {
			b.WriteString(" (ошибка: " + st.Error + ")")
		}
		b.WriteString("\n")
	}
	return b.String()
}
