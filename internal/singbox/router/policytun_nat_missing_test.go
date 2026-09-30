package router

import (
	"context"
	"errors"
	"fmt"
	"testing"

	"github.com/hoaxisr/awg-manager/internal/storage"
)

// Сегмент, удалённый пользователем, пока policy-tun работал: восстанавливать
// на нём нечего, и это не провал восстановления. Иначе запись сегмента
// остаётся в персисте навсегда — автоматической повторной попытки у неё нет.
func TestRestorePolicyTunNAT_SkipsVanishedSegment(t *testing.T) {
	svc, _ := newOrchedTestService(t)
	svc.deps.NATState = &fakeNATState{}
	svc.deps.DefaultGateway = &fakeGateway{name: "PPPoE0"}
	svc.deps.SegmentNAT = &errSegmentNAT{err: errors.New(`ip nat Guest: router reported error: no "Guest" IP interface found.`)}

	err := svc.restorePolicyTunNAT(context.Background(), []storage.PolicyTunNATSegment{
		{Name: "Guest", PriorMode: natModeDynamic},
	})
	if err != nil {
		t.Errorf("исчезнувший сегмент считается провалом восстановления: %v", err)
	}
}

// Настоящий отказ восстановления по-прежнему всплывает.
func TestRestorePolicyTunNAT_SurfacesRealError(t *testing.T) {
	svc, _ := newOrchedTestService(t)
	svc.deps.NATState = &fakeNATState{}
	svc.deps.DefaultGateway = &fakeGateway{name: "PPPoE0"}
	svc.deps.SegmentNAT = &errSegmentNAT{err: errors.New("ip nat Guest: router reported error: segment is busy")}

	err := svc.restorePolicyTunNAT(context.Background(), []storage.PolicyTunNATSegment{
		{Name: "Guest", PriorMode: natModeDynamic},
	})
	if err == nil {
		t.Error("настоящий отказ восстановления проглочен")
	}
}

// Прежний выход static снят пользователем (PPPoE0 удалён): при восстановлении
// возвращать static не на что — не провал, иначе запись сегмента не снимается
// никогда и отзыв предупреждает каждый тик (F563).
func TestRestorePolicyTunNAT_PriorWANAbsent_NotFailure(t *testing.T) {
	svc, _ := newOrchedTestService(t)
	svc.deps.NATState = &fakeNATState{}
	svc.deps.SegmentNAT = &errSegmentNAT{err: fmt.Errorf("WAN PPPoE0: %w", ErrWANAbsent)}

	err := svc.restorePolicyTunNAT(context.Background(), []storage.PolicyTunNATSegment{
		{Name: "Guest", PriorMode: natModeStatic, PriorStaticWAN: "PPPoE0"},
	})
	if err != nil {
		t.Errorf("исчезнувший прежний WAN считается провалом восстановления: %v", err)
	}
}

// На включении отсутствующий выход — ошибка, а не «сегмент снят» (F563).
func TestApplySourcePreserve_WANAbsent_Fails(t *testing.T) {
	svc, _ := newOrchedTestService(t)
	svc.deps.NATState = &fakeNATState{}
	svc.deps.DefaultGateway = &fakeGateway{name: "PPPoE0"}
	svc.deps.SegmentNAT = &errSegmentNAT{err: fmt.Errorf("WAN PPPoE0: %w", ErrWANAbsent)}

	if _, err := svc.applyPolicyTunSourcePreserve(context.Background(), []string{"Guest"}, nil); err == nil {
		t.Error("отсутствующий выход проглочен как снятый сегмент")
	}
}

type errSegmentNAT struct{ err error }

func (e *errSegmentNAT) SetSegmentNAT(context.Context, string) error           { return e.err }
func (e *errSegmentNAT) RemoveSegmentNAT(context.Context, string) error        { return e.err }
func (e *errSegmentNAT) SetStaticNAT(context.Context, string, string) error    { return e.err }
func (e *errSegmentNAT) RemoveStaticNAT(context.Context, string, string) error { return e.err }

// Тот же дрейф на включении: сегмент из желаемого набора исчез с роутера.
// Он пропускается вместе со своей записью — включение policy-tun не должно
// падать целиком, а восстанавливать на нём потом всё равно нечего.
func TestApplySourcePreserve_SkipsVanishedSegment(t *testing.T) {
	svc, _ := newOrchedTestService(t)
	svc.deps.NATState = &fakeNATState{}
	svc.deps.DefaultGateway = &fakeGateway{name: "PPPoE0"}
	svc.deps.SegmentNAT = &errSegmentNAT{
		err: errors.New(`ip static Guest PPPoE0: router reported error: unknown interface "Guest".`),
	}

	recorded, err := svc.applyPolicyTunSourcePreserve(context.Background(), []string{"Guest"}, nil)
	if err != nil {
		t.Fatalf("исчезнувший сегмент уронил включение: %v", err)
	}
	if len(recorded) != 0 {
		t.Errorf("записан сегмент, которого нет: %+v", recorded)
	}
}

// Адаптер подтверждения (cmd/awg-manager, ErrIfaceAbsent) не шлёт команду по
// отсутствующему сегменту и отвечает своим текстом — это тот же дрейф.
func TestApplySourcePreserve_SkipsSegmentAbsentByConfirm(t *testing.T) {
	svc, _ := newOrchedTestService(t)
	svc.deps.NATState = &fakeNATState{}
	svc.deps.DefaultGateway = &fakeGateway{name: "PPPoE0"}
	svc.deps.SegmentNAT = &errSegmentNAT{err: fmt.Errorf("интерфейса Guest %w", ErrIfaceAbsent)}

	recorded, err := svc.applyPolicyTunSourcePreserve(context.Background(), []string{"Guest"}, nil)
	if err != nil {
		t.Fatalf("исчезнувший сегмент уронил включение: %v", err)
	}
	if len(recorded) != 0 {
		t.Errorf("записан сегмент, которого нет: %+v", recorded)
	}
}
