package router

import (
	"context"
	"errors"
	"testing"

	"github.com/hoaxisr/awg-manager/internal/storage"
)

// RT3: провал установки правил НЕ коммитит снимок применённого состояния.
//
// Инвариант (F20, комментарий в service_lifecycle.go): restore мог применить
// ЧАСТЬ таблиц, поэтому после отказа реальное состояние netfilter неизвестно.
// Закоммитить `appliedSpec` и `netfilterStateKnown = true` значило бы соврать
// самим себе: следующий тик увидит «желаемое совпадает с применённым», решит,
// что делать нечего, и перехват останется не установленным — до ручного
// Enable или рестарта демона. Оговорка по ревью: «навсегда» верно не всегда —
// если частичный restore СНЁС PREROUTING-джампы, следующий тик заметит это
// пробой и переустановит. Опасен подслучай, где джампы уцелели: тогда пробы
// говорят «всё на месте», а правил за ними нет.
//
// У blackhole ровно этот сценарий запинен, у основного перехвата не был:
// мутация «коммитить снимок в ветке ошибки» оставляла весь пакет зелёным.
func TestReconcileInstalled_InstallFailureDoesNotCommitSnapshot(t *testing.T) {
	stubNoLANBridges(t)
	boom := errors.New("iptables-restore: отказ")
	ipt := newStubIPTables(func(context.Context, string) error { return boom })
	stubListeningProbe(t, func() bool { return true })

	// Состояние ДО: снимок от прежнего успешного применения. Именно его
	// отказ не имеет права подменить своим `want`.
	prev := &RestoreInputSpec{PolicyMark: "0xffffaaa", WANIPs: []string{"203.0.113.1/32"}}
	svc := &ServiceImpl{
		deps: Deps{
			Policies:           &fakeAccessPolicyProvider{mark: "0xffffbbb"},
			IPTables:           ipt,
			WANIPCollector:     &fakeWANIPCollector{ips: []string{"198.51.100.9/32"}},
			Singbox:            newReadyTestSingbox(t),
			NetfilterPreflight: func(context.Context) error { return nil },
		},
		appliedSpec:         prev,
		netfilterStateKnown: true,
	}
	sr := storage.SingboxRouterSettings{Enabled: true, PolicyName: "Policy0", WANAutoDetect: true}

	err := svc.reconcileInstalled(context.Background(), sr)
	if err == nil {
		t.Fatal("отказ установки правил обязан дойти до вызывающего")
	}
	if !errors.Is(err, boom) {
		t.Fatalf("причина отказа подменена: %v", err)
	}

	// Главное: знание о состоянии сброшено — следующий тик обязан
	// переустановить правила, а не считать, что всё на месте.
	if svc.netfilterStateKnown {
		t.Error("после отказа состояние netfilter НЕИЗВЕСТНО: known обязан быть false")
	}
	// И снимок не подменён желаемым: иначе сравнение «want == applied»
	// на следующем тике совпадёт, и переустановки не будет никогда.
	if svc.appliedSpec != nil && svc.appliedSpec.PolicyMark == "0xffffbbb" {
		t.Errorf("снимок закоммичен вопреки отказу: %+v", svc.appliedSpec)
	}
}
