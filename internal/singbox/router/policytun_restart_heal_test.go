package router

import (
	"context"
	"strings"
	"testing"
)

// Разрешение трафика в tun — это NDMS-native список доступа
// `_WEBADMIN_OpkgTunN` плюс привязка к интерфейсу (ndms/command/acl.go:85).
// Без него firewall роутера режет трафик членов политики ДО того, как он
// дойдёт до sing-box: движок здоров, интерфейс поднят, дефолт припаркован, а
// Clash API показывает ноль соединений.
//
// Остальные четыре операции режима (ip global, дефолт v4/v6, permit выходом
// политики) drift-heal сверяет с running-config и чинит расхождение. ACL
// сверялся с булевым полем в памяти процесса, которое никто не сбрасывает и
// которое не привязано к имени интерфейса: после смены индекса OpkgTun список
// для нового имени не создавался уже никогда.
//
// Назначение списка идемпотентно (дубликат гасится IsACLDuplicate), поэтому
// ассерт делается на каждом тике, как и остальные четыре.
func TestPolicyTunReconcile_ReassertsPermitACLEveryTick(t *testing.T) {
	h := newPolicyTunEnableHarness(t, "")
	h.withPolicy(t, "Policy0")

	ctx := context.Background()
	if err := h.svc.Enable(ctx); err != nil {
		t.Fatalf("Enable(policy-tun): %v", err)
	}

	st := h.loadPolicyTun(t)
	if st == nil || !st.Provisioned {
		t.Fatalf("предусловие: персист = %+v, ожидался Provisioned", st)
	}
	// Индекс жив в ядре — reconcile идёт в drift-heal, а не в re-provision.
	h.svc.deps.OpkgTunIndices = &recIndices{live: map[int]bool{st.Index: true}}

	// Первый тик: ассерт отрабатывает и до фикса (флаг ещё не взведён).
	if err := h.svc.Reconcile(ctx); err != nil {
		t.Fatalf("Reconcile #1: %v", err)
	}

	ndmsName := tunNDMSName(st.Index)
	want := []string{"SetPermitACL:" + ndmsName, "SetPermitACLv6:" + ndmsName}
	mark := len(h.log.calls)

	// Второй тик: именно здесь одноразовый флаг гасил ассерт навсегда.
	if err := h.svc.Reconcile(ctx); err != nil {
		t.Fatalf("Reconcile #2: %v", err)
	}

	after := h.log.calls[mark:]
	var missing []string
	for _, c := range want {
		if !containsCall(after, c) {
			missing = append(missing, c)
		}
	}
	if len(missing) > 0 {
		t.Errorf("на втором тике drift-heal не переиздал %s\nвызовы Reconcile #2: %v",
			strings.Join(missing, ", "), after)
	}
}

func containsCall(calls []string, want string) bool {
	for _, c := range calls {
		if c == want {
			return true
		}
	}
	return false
}
