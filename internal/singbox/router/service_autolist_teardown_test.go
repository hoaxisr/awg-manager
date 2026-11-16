package router

import (
	"context"
	"errors"
	"testing"
)

func newTeardownTestService(t *testing.T) *ServiceImpl {
	t.Helper()
	svc, dir := newOrchedTestService(t)
	// Материализатору inline-набора нужен «бинарник» sing-box; компилятор
	// подменяется ниже, поэтому достаточно непустого имени.
	svc.deps.Singbox = &fakeSingbox{dir: dir, binary: "sb-stub"}
	return svc
}

func addAutolistObjects(t *testing.T, svc *ServiceImpl, withForeignRule bool) {
	t.Helper()
	withFakeRuleSetCompiler(t, func(_ string, args []string) (string, string, error) {
		writeCompiledOutput(t, args, "compiled")
		return "", "", nil
	})
	ctx := context.Background()
	if err := svc.AddCompositeOutbound(ctx, Outbound{Type: "direct", Tag: "awgm-autolist-target", BindInterface: "eth3"}); err != nil {
		t.Fatal(err)
	}
	if err := svc.AddRuleSet(ctx, RuleSet{
		Tag:   "awgm-autolist",
		Type:  "inline",
		Rules: []map[string]any{{"domain_suffix": []any{"example.com"}}},
	}); err != nil {
		t.Fatal(err)
	}
	if err := svc.AddRule(ctx, Rule{RuleSet: []string{"awgm-autolist"}, Action: "route", Outbound: "awgm-autolist-target"}); err != nil {
		t.Fatal(err)
	}
	if withForeignRule {
		if err := svc.AddRule(ctx, Rule{DomainSuffix: []string{"foreign.example"}, Action: "route", Outbound: "awgm-autolist-target"}); err != nil {
			t.Fatal(err)
		}
	}
}

func assertTeardownState(t *testing.T, svc *ServiceImpl, wantGone bool) {
	t.Helper()
	ctx := context.Background()
	rules, err := svc.ListRules(ctx)
	if err != nil {
		t.Fatal(err)
	}
	sets, err := svc.ListRuleSets(ctx)
	if err != nil {
		t.Fatal(err)
	}
	outs, err := svc.ListCompositeOutbounds(ctx)
	if err != nil {
		t.Fatal(err)
	}
	ownerLeft, setLeft, ruleRefs, tagLeft := 0, 0, 0, 0
	for _, r := range rules {
		if isAutolistOwnerRule(r) {
			ownerLeft++
		}
		if teardownRuleRefsOutbound(r, "awgm-autolist-target") {
			ruleRefs++
		}
	}
	for _, rs := range sets {
		if rs.Tag == "awgm-autolist" {
			setLeft++
		}
	}
	for _, ob := range outs {
		if ob.Tag == "awgm-autolist-target" {
			tagLeft++
		}
	}
	// Правило-ссылка и сам outbound — РАЗНЫЕ сущности: считаем их отдельно.
	//wantGone: не осталось ни ссылок, ни самого outbound.
	if wantGone && (ownerLeft != 0 || setLeft != 0 || ruleRefs != 0 || tagLeft != 0) {
		t.Fatalf("объекты должны отсутствовать: owner=%d set=%d ruleRefs=%d outbound=%d", ownerLeft, setLeft, ruleRefs, tagLeft)
	}
	if !wantGone && (ownerLeft < 1 || setLeft != 1 || ruleRefs < 1 || tagLeft != 1) {
		t.Fatalf("объекты должны остаться нетронутыми: owner=%d set=%d ruleRefs=%d outbound=%d", ownerLeft, setLeft, ruleRefs, tagLeft)
	}
}

func TestTeardownAutolistObjects_HappyPath(t *testing.T) {
	svc := newTeardownTestService(t)
	addAutolistObjects(t, svc, false)
	res, err := svc.TeardownAutolistObjects(context.Background())
	if err != nil {
		t.Fatalf("teardown: %v", err)
	}
	if !res.BackupSaved {
		t.Error("снимок должен сохраниться в каталоге данных панели")
	}
	assertTeardownState(t, svc, true)
	if svc.StagingStatus(context.Background()).HasDraft {
		t.Error("после teardown не должно оставаться неприменённого черновика")
	}
}

func TestTeardownAutolistObjects_Idempotent(t *testing.T) {
	svc := newTeardownTestService(t)
	// Ни одного объекта Autolist в пустом конфиге — снос проходит без ошибки
	// и без apply (черновика нет).
	res, err := svc.TeardownAutolistObjects(context.Background())
	if err != nil {
		t.Fatalf("teardown on empty config: %v", err)
	}
	if svc.StagingStatus(context.Background()).HasDraft {
		t.Error("нечего удалять — черновик не должен создаваться")
	}
	// Нечего снимать: устаревшего файла быть не должно.
	if exists, _, err := svc.AutolistBackupInfo(context.Background()); err != nil {
		t.Fatalf("AutolistBackupInfo: %v", err)
	} else if exists {
		t.Error("пустой конфиг не должен оставлять снимок")
	}
	_ = res
}

func TestTeardownAutolistObjects_ForeignRuleRefuses(t *testing.T) {
	svc := newTeardownTestService(t)
	addAutolistObjects(t, svc, true)
	_, err := svc.TeardownAutolistObjects(context.Background())
	if !errors.Is(err, ErrOutboundReferenced) {
		t.Fatalf("want ErrOutboundReferenced, got %v", err)
	}
	// Отказ до мутаций: ничего не удалено (apply не звался, объектов нет).
	assertTeardownState(t, svc, false)
	// Черновик мог появиться ещё при setup (Add* пишут draft); после отказа
	// сноса он не обязан исчезать — отказ не применяет и не сбрасывает его.
}

func TestTeardownAutolistObjects_TwoOwnerRulesRefuses(t *testing.T) {
	svc := newTeardownTestService(t)
	addAutolistObjects(t, svc, false)
	// Второе безусловное owner-правило — неоднозначность выбора.
	if err := svc.AddRule(context.Background(), Rule{RuleSet: []string{"awgm-autolist"}, Action: "route", Outbound: "awgm-autolist-target"}); err != nil {
		t.Fatal(err)
	}
	if _, err := svc.TeardownAutolistObjects(context.Background()); err == nil {
		t.Fatal("два owner-правила должны отклонять снос")
	}
	assertTeardownState(t, svc, false)
}
