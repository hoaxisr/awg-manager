package router

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/hoaxisr/awg-manager/internal/netdev"
	"github.com/hoaxisr/awg-manager/internal/storage"
)

// setPolicyTunDescription кладёт в стор желаемое описание policy-tun и отдаёт
// нормализованные настройки — те, что reconcile получает от Reconcile.
func setPolicyTunDescription(t *testing.T, store *storage.SettingsStore, desc string) storage.SingboxRouterSettings {
	t.Helper()
	if err := store.Update(func(cur *storage.Settings) error {
		cur.SingboxRouter.PolicyTunDescription = desc
		return nil
	}); err != nil {
		t.Fatalf("Update: %v", err)
	}
	all, err := store.Load()
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	sr, err := NormalizeSingboxRouterSettings(all.SingboxRouter)
	if err != nil {
		t.Fatalf("Normalize: %v", err)
	}
	return sr
}

// assertNoReprovision — переименование не имеет права пересоздавать или
// сносить интерфейс: удаление рвёт permit'ы пользователя в политиках.
func assertNoReprovision(t *testing.T, log *callLog) {
	t.Helper()
	for _, c := range log.calls {
		if strings.HasPrefix(c, "Create:") || strings.HasPrefix(c, "Delete:") {
			t.Fatalf("интерфейс пересоздан или снесён: %v", log.calls)
		}
	}
}

func TestNormalizePolicyTunDescription(t *testing.T) {
	cases := []struct {
		in      string
		want    string
		wantErr bool
	}{
		{in: "", want: ""},
		{in: "Awgmanager", want: "Awgmanager"},
		{in: "  Awgmanager  ", want: "Awgmanager"},
		// Штатное хранится пустым: одно значение — одно представление.
		{in: "awgm policy-tun", want: ""},
		{in: " awgm policy-tun ", want: ""},
		{in: "Мой туннель", want: "Мой туннель"},
		{in: strings.Repeat("я", policyTunDescriptionMaxRunes), want: strings.Repeat("я", policyTunDescriptionMaxRunes)},
		{in: strings.Repeat("a", policyTunDescriptionMaxRunes+1), wantErr: true},
		{in: "a\tb", wantErr: true},
		// Форматирующие — не управляющие по IsControl, но невидимые или
		// меняющие порядок чтения: U+202E (RLO), U+200B (zero width space).
		{in: "a‮b", wantErr: true},
		{in: "a​b", wantErr: true},
		// Служебный префикс: чужой штамп режима и метки прокси-рантайма,
		// которые уборщик ищет по префиксу.
		{in: "awgm fakeip-tun", wantErr: true},
		{in: "AWGM WDTT", wantErr: true},
		{in: "Awgm x", wantErr: true},
		// Без пробела — не служебная метка.
		{in: "AWGM-tun", want: "AWGM-tun"},
	}
	for _, c := range cases {
		sr := storage.SingboxRouterSettings{PolicyTunDescription: c.in}
		err := normalizePolicyTunDescription(&sr)
		if c.wantErr {
			if err == nil {
				t.Errorf("%q: ожидалась ошибка, получено %q", c.in, sr.PolicyTunDescription)
			}
			continue
		}
		if err != nil {
			t.Errorf("%q: %v", c.in, err)
			continue
		}
		if sr.PolicyTunDescription != c.want {
			t.Errorf("%q → %q, want %q", c.in, sr.PolicyTunDescription, c.want)
		}
	}
}

// Настройки PUT идут через Normalize — недопустимое имя отвергается до
// персиста, штатное сохраняется пустым.
func TestNormalizeSingboxRouterSettings_PolicyTunDescription(t *testing.T) {
	base := storage.SingboxRouterSettings{WANAutoDetect: true}

	sr := base
	sr.PolicyTunDescription = "AWGM WDTT Raw"
	if _, err := NormalizeSingboxRouterSettings(sr); err == nil {
		t.Error("служебный префикс обязан отвергаться")
	}

	sr = base
	sr.PolicyTunDescription = " awgm policy-tun "
	got, err := NormalizeSingboxRouterSettings(sr)
	if err != nil {
		t.Fatalf("Normalize: %v", err)
	}
	if got.PolicyTunDescription != "" {
		t.Errorf("штатное описание = %q, want пусто", got.PolicyTunDescription)
	}
}

// Набор владения — ТОЛЬКО из записи: применённое и намерение. Желаемого из
// настроек в нём нет (см. TestReconcilePolicyTun_SameNameForeignIsNotAdopted).
func TestPolicyTunOwnDescriptions(t *testing.T) {
	cases := []struct {
		name string
		st   *storage.OpkgTunState
		want []string
	}{
		{"нет записи", nil, []string{policyTunDescription}},
		{"запись без описания (старая версия)", pendingRec("", ""), []string{policyTunDescription}},
		{"применено", pendingRec("Awgmanager", ""), []string{"Awgmanager"}},
		{"намерение из дефолта", pendingRec("", "B"), []string{policyTunDescription, "B"}},
		{"намерение с пользовательского", pendingRec("A", "B"), []string{"A", "B"}},
		{"намерение — штатное имя", pendingRec("Awgmanager", policyTunDescription), []string{"Awgmanager", policyTunDescription}},
		{"намерение совпадает с применённым", pendingRec("A", "A"), []string{"A"}},
	}
	for _, c := range cases {
		if got := policyTunOwnDescriptions(c.st); !reflect.DeepEqual(got, c.want) {
			t.Errorf("%s: %v, want %v", c.name, got, c.want)
		}
	}
}

// Желаемое из настроек — не метка владения. Наш интерфейс пропал (пользователь
// удалил его из веб-интерфейса роутера), и на том же номере он создал СВОЙ
// OpkgTun с тем же названием, что стоит в наших настройках. Это чужой
// интерфейс: режим переподнимается на другом номере, а его не трогаем — ни
// переименованием, ни сносом.
func TestReconcilePolicyTun_SameNameForeignIsNotAdopted(t *testing.T) {
	h := newPolicyTunEnableHarness(t, "")
	provisionPolicyTunForReconcile(t, h) // запись: индекс 0, штатное описание
	h.svc.deps.RunningConfig = &fakeRunningConfig{lines: healthyPolicyTunRC("OpkgTun0")}
	sr := setPolicyTunDescription(t, h.store, "Awgmanager")
	// NDMS: под штатным описанием никого, под «Awgmanager» — чужой на нашем
	// номере.
	h.svc.deps.OpkgTunScan = scanOurs("Awgmanager", "OpkgTun0")

	if err := h.svc.reconcilePolicyTun(context.Background(), sr); err != nil {
		t.Fatalf("reconcilePolicyTun: %v", err)
	}
	if !h.log.has("Create:OpkgTun1:public") {
		t.Errorf("чужой интерфейс с нашим названием обязан дать re-provision на другом номере: %v", h.log.calls)
	}
	for _, call := range h.log.calls {
		if strings.HasPrefix(call, "SetDescription:OpkgTun0:") || call == "Delete:OpkgTun0" {
			t.Errorf("чужой интерфейс тронут: %v", h.log.calls)
		}
	}
}

// pendingRec — запись владения с незакрытым намерением переименования.
func pendingRec(applied, pend string) *storage.OpkgTunState {
	return &storage.OpkgTunState{Mode: storage.OpkgTunModePolicyTun, Description: applied, PendingDescription: pend}
}

// Сценарий, ради которого намерение пишется на флеш: переименование A→B дошло
// до NDMS, запись подтвердить не удалось, и до следующего тика имя в
// настройках сменили на C. Интерфейс стоит под B — и признаётся своим по
// намерению, а не объявляется чужим с re-provision на другом номере.
func TestReconcilePolicyTun_PendingRenameSurvivesSettingChange(t *testing.T) {
	h := newPolicyTunEnableHarness(t, "")
	provisionPolicyTunForReconcile(t, h)
	h.svc.deps.RunningConfig = &fakeRunningConfig{lines: healthyPolicyTunRC("OpkgTun0")}
	if err := h.store.SetOpkgTunState(&storage.OpkgTunState{
		Mode: storage.OpkgTunModePolicyTun, Provisioned: true, Index: 0, PendingDescription: "B",
	}); err != nil {
		t.Fatalf("SetOpkgTunState: %v", err)
	}
	h.svc.deps.OpkgTunScan = scanOurs("B", "OpkgTun0") // NDMS: уже под B
	sr := setPolicyTunDescription(t, h.store, "C")

	if err := h.svc.reconcilePolicyTun(context.Background(), sr); err != nil {
		t.Fatalf("reconcilePolicyTun: %v", err)
	}
	assertNoReprovision(t, h.log)
	if !h.log.has("SetDescription:OpkgTun0:C") {
		t.Errorf("интерфейс под B обязан переименоваться в C: %v", h.log.calls)
	}
	if st := h.loadPolicyTun(t); st == nil || st.Index != 0 || st.Description != "C" || st.PendingDescription != "" {
		t.Errorf("PolicyTun persist = %+v, want index 0, Description=C, намерение снято", st)
	}
}

// Намерение, не дошедшее до NDMS (крах до SetDescription): интерфейс так и
// стоит под применённым именем. Намерение снимается, переименование идёт с
// применённого — без re-provision.
func TestReconcilePolicyTun_StalePendingIsDropped(t *testing.T) {
	cases := []struct {
		name       string
		want       string // настройка
		wantRename string // ожидаемый SetDescription, "" — не должно быть
	}{
		{"настройка сменилась", "C", "C"},
		{"настройка вернулась к штатному", "", ""},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			h := newPolicyTunEnableHarness(t, "")
			provisionPolicyTunForReconcile(t, h)
			h.svc.deps.RunningConfig = &fakeRunningConfig{lines: healthyPolicyTunRC("OpkgTun0")}
			if err := h.store.SetOpkgTunState(&storage.OpkgTunState{
				Mode: storage.OpkgTunModePolicyTun, Provisioned: true, Index: 0, PendingDescription: "B",
			}); err != nil {
				t.Fatalf("SetOpkgTunState: %v", err)
			}
			// NDMS: всё ещё штатное — и ТОЛЬКО оно (скан по B пуст).
			h.svc.deps.OpkgTunScan = scanOurs(policyTunDescription, "OpkgTun0")
			sr := setPolicyTunDescription(t, h.store, c.want)

			if err := h.svc.reconcilePolicyTun(context.Background(), sr); err != nil {
				t.Fatalf("reconcilePolicyTun: %v", err)
			}
			assertNoReprovision(t, h.log)
			renamed := false
			for _, call := range h.log.calls {
				if strings.HasPrefix(call, "SetDescription:") {
					renamed = true
					if c.wantRename == "" || call != "SetDescription:OpkgTun0:"+c.wantRename {
						t.Errorf("неожиданное переименование %q (want %q)", call, c.wantRename)
					}
				}
			}
			if c.wantRename != "" && !renamed {
				t.Errorf("переименование в %q ожидалось: %v", c.wantRename, h.log.calls)
			}
			if st := h.loadPolicyTun(t); st == nil || st.Description != c.want || st.PendingDescription != "" {
				t.Errorf("PolicyTun persist = %+v, want Description=%q, намерение снято", st, c.want)
			}
		})
	}
}

// Скан упал, пока намерение не закрыто: дошло ли переименование до NDMS,
// неизвестно — ни мутаций, ни правок записи, повтор следующим тиком.
func TestReconcilePolicyTun_PendingKeptWhenScanFails(t *testing.T) {
	h := newPolicyTunEnableHarness(t, "")
	provisionPolicyTunForReconcile(t, h)
	h.svc.deps.RunningConfig = &fakeRunningConfig{lines: healthyPolicyTunRC("OpkgTun0")}
	if err := h.store.SetOpkgTunState(&storage.OpkgTunState{
		Mode: storage.OpkgTunModePolicyTun, Provisioned: true, Index: 0, PendingDescription: "B",
	}); err != nil {
		t.Fatalf("SetOpkgTunState: %v", err)
	}
	h.svc.deps.OpkgTunScan = func(context.Context, string) ([]string, error) {
		return nil, errors.New("injected: scan")
	}
	sr := setPolicyTunDescription(t, h.store, "C")

	if err := h.svc.reconcilePolicyTun(context.Background(), sr); err != nil {
		t.Fatalf("reconcilePolicyTun: %v", err)
	}
	assertNoReprovision(t, h.log)
	for _, call := range h.log.calls {
		if strings.HasPrefix(call, "SetDescription:") {
			t.Fatalf("без доказанного владения переименования быть не должно: %v", h.log.calls)
		}
	}
	if st := h.loadPolicyTun(t); st == nil || st.Description != "" || st.PendingDescription != "B" {
		t.Errorf("PolicyTun persist = %+v, want запись нетронута (намерение B)", st)
	}
}

// Несколько описаний: наш — если имя нашлось хоть в одном скане; упавший скан
// без находки — «не знаем», а не «чужой» (F493).
func TestOpkgTunOwnership_MultipleDescriptions(t *testing.T) {
	scanErr := errors.New("rci down")
	cases := []struct {
		name string
		ids  map[string][]string
		errs map[string]error
		want opkgTunOwnership
	}{
		{"наш под вторым описанием", map[string][]string{"new": {"OpkgTun0"}}, nil, ownershipOurs},
		{"первый скан упал, второй нашёл", map[string][]string{"new": {"OpkgTun0"}}, map[string]error{"old": scanErr}, ownershipOurs},
		{"первый скан упал, второй пуст", nil, map[string]error{"old": scanErr}, ownershipUnknown},
		{"оба пусты", map[string][]string{"old": {"OpkgTun5"}}, nil, ownershipForeign},
	}
	for _, c := range cases {
		scan := func(_ context.Context, desc string) ([]string, error) {
			if err := c.errs[desc]; err != nil {
				return nil, err
			}
			return c.ids[desc], nil
		}
		svc := newTestService(t, Deps{OpkgTunScan: scan})
		if got := svc.opkgTunOwnership(context.Background(), "OpkgTun0", "old", "new"); got != c.want {
			t.Errorf("%s: %v, want %v", c.name, got, c.want)
		}
	}
}

// Без описаний скана не было — «доказанно чужой» недоказуем: вердикт «не
// знаем», роутер не опрашивается.
func TestOpkgTunOwnership_NoDescriptionsIsUnknown(t *testing.T) {
	scans := 0
	scan := func(context.Context, string) ([]string, error) {
		scans++
		return nil, nil
	}
	svc := newTestService(t, Deps{OpkgTunScan: scan})
	if got := svc.opkgTunOwnership(context.Background(), "OpkgTun0"); got != ownershipUnknown {
		t.Fatalf("ownership = %v, want %v", got, ownershipUnknown)
	}
	if scans != 0 {
		t.Fatalf("scans = %d, want 0", scans)
	}
}

// Новый интерфейс получает имя из настроек, и запись владения помнит его уже к
// моменту Create (persist-before-create).
func TestPolicyTunEnable_CustomDescription(t *testing.T) {
	h := newPolicyTunEnableHarness(t, "")
	setPolicyTunDescription(t, h.store, "Awgmanager")
	probe := &createPersistProbe{OpkgTunProvisioner: h.svc.deps.OpkgTun, store: h.store}
	h.svc.deps.OpkgTun = probe

	if err := h.svc.Enable(context.Background()); err != nil {
		t.Fatalf("Enable(policy-tun): %v", err)
	}
	if len(h.opkg.descs) != 1 || h.opkg.descs[0] != "Awgmanager" {
		t.Errorf("Create description = %v, want [Awgmanager]", h.opkg.descs)
	}
	if probe.atCreate == nil || probe.atCreate.Description != "Awgmanager" {
		t.Errorf("запись в момент Create = %+v, want Description=Awgmanager", probe.atCreate)
	}
	if st := h.loadPolicyTun(t); st == nil || st.Description != "Awgmanager" {
		t.Errorf("PolicyTun persist = %+v, want Description=Awgmanager", st)
	}
}

// Удержанный свой интерфейс под прежним именем переиспользуется и
// переименовывается на включении. До Create запись называет ПРЕЖНЕЕ имя —
// то, что на интерфейсе сейчас; после — новое.
func TestPolicyTunEnable_RenamesHeldOwnInterface(t *testing.T) {
	h := newPolicyTunEnableHarness(t, "")
	h.svc.deps.OpkgTunIndices = &recIndices{live: map[int]bool{3: true}}
	h.svc.deps.OpkgTunScan = scanOwning("OpkgTun3") // NDMS: штатное описание
	if err := h.store.SetOpkgTunState(&storage.OpkgTunState{Mode: storage.OpkgTunModePolicyTun, Index: 3}); err != nil {
		t.Fatalf("SetOpkgTunState: %v", err)
	}
	setPolicyTunDescription(t, h.store, "Awgmanager")
	probe := &createPersistProbe{OpkgTunProvisioner: h.svc.deps.OpkgTun, store: h.store}
	h.svc.deps.OpkgTun = probe

	if err := h.svc.Enable(context.Background()); err != nil {
		t.Fatalf("Enable(policy-tun): %v", err)
	}
	if !h.log.has("Create:OpkgTun3:public") {
		t.Fatalf("удержанный свой индекс 3 обязан переиспользоваться: %v", h.log.calls)
	}
	if probe.atCreate == nil || probe.atCreate.Description != "" || probe.atCreate.PendingDescription != "Awgmanager" {
		t.Errorf("запись в момент Create = %+v, want прежнее (штатное) описание и намерение Awgmanager", probe.atCreate)
	}
	if st := h.loadPolicyTun(t); st == nil || st.Index != 3 || st.Description != "Awgmanager" || st.PendingDescription != "" {
		t.Errorf("PolicyTun persist = %+v, want index 3, Description=Awgmanager, намерение снято", st)
	}
}

// Переименование живого интерфейса — это SetDescription на тике reconcile, а
// не пересоздание: permit'ы в политиках привязаны к объекту.
func TestReconcilePolicyTun_RenamesLiveInterface(t *testing.T) {
	h := newPolicyTunEnableHarness(t, "")
	provisionPolicyTunForReconcile(t, h)
	h.svc.deps.RunningConfig = &fakeRunningConfig{lines: healthyPolicyTunRC("OpkgTun0")}
	h.svc.deps.OpkgTunScan = scanOwning("OpkgTun0") // NDMS: пока штатное описание
	sr := setPolicyTunDescription(t, h.store, "Awgmanager")

	if err := h.svc.reconcilePolicyTun(context.Background(), sr); err != nil {
		t.Fatalf("reconcilePolicyTun: %v", err)
	}
	if !h.log.has("SetDescription:OpkgTun0:Awgmanager") {
		t.Errorf("живой интерфейс обязан переименоваться: %v", h.log.calls)
	}
	assertNoReprovision(t, h.log)
	if st := h.loadPolicyTun(t); st == nil || !st.Provisioned || st.Index != 0 || st.Description != "Awgmanager" || st.PendingDescription != "" {
		t.Errorf("PolicyTun persist = %+v, want provisioned index 0, Description=Awgmanager, намерение снято", st)
	}

	// Следующий тик: NDMS уже под новым именем — ни одной мутации.
	h.svc.deps.OpkgTunScan = scanOurs("Awgmanager", "OpkgTun0")
	h.log.calls = nil
	if err := h.svc.reconcilePolicyTun(context.Background(), sr); err != nil {
		t.Fatalf("reconcilePolicyTun (второй тик): %v", err)
	}
	if len(h.log.calls) != 0 {
		t.Errorf("применённое имя не требует мутаций, получено %v", h.log.calls)
	}
}

// Возврат к штатному имени — тот же путь, и запись снова хранит его пустым.
func TestReconcilePolicyTun_RenamesBackToDefault(t *testing.T) {
	h := newPolicyTunEnableHarness(t, "")
	setPolicyTunDescription(t, h.store, "Awgmanager")
	provisionPolicyTunForReconcile(t, h)
	h.svc.deps.RunningConfig = &fakeRunningConfig{lines: healthyPolicyTunRC("OpkgTun0")}
	h.svc.deps.OpkgTunScan = scanOurs("Awgmanager", "OpkgTun0")
	sr := setPolicyTunDescription(t, h.store, "")

	if err := h.svc.reconcilePolicyTun(context.Background(), sr); err != nil {
		t.Fatalf("reconcilePolicyTun: %v", err)
	}
	if !h.log.has("SetDescription:OpkgTun0:" + policyTunDescription) {
		t.Errorf("интерфейс обязан вернуться к штатному имени: %v", h.log.calls)
	}
	assertNoReprovision(t, h.log)
	if st := h.loadPolicyTun(t); st == nil || st.Description != "" {
		t.Errorf("PolicyTun persist = %+v, want пустое описание (штатное)", st)
	}
}

// Крах между переименованием в NDMS и записью результата: интерфейс уже под
// новым именем, запись — под прежним, намерение не закрыто. Владение признаёт
// намерение, поэтому тик дописывает запись, а не провижинит режим заново
// поверх живого интерфейса; и не переименовывает повторно — скан по
// намерению показал, что оно дошло (без этой сверки SetDescription шёл бы в
// NDMS на каждом таком тике).
func TestReconcilePolicyTun_RenamedButNotPersisted(t *testing.T) {
	h := newPolicyTunEnableHarness(t, "")
	provisionPolicyTunForReconcile(t, h)
	h.svc.deps.RunningConfig = &fakeRunningConfig{lines: healthyPolicyTunRC("OpkgTun0")}
	if err := h.store.SetOpkgTunState(&storage.OpkgTunState{
		Mode: storage.OpkgTunModePolicyTun, Provisioned: true, Index: 0, PendingDescription: "Awgmanager",
	}); err != nil {
		t.Fatalf("SetOpkgTunState: %v", err)
	}
	h.svc.deps.OpkgTunScan = scanOurs("Awgmanager", "OpkgTun0") // NDMS уже переименован
	sr := setPolicyTunDescription(t, h.store, "Awgmanager")

	if err := h.svc.reconcilePolicyTun(context.Background(), sr); err != nil {
		t.Fatalf("reconcilePolicyTun: %v", err)
	}
	assertNoReprovision(t, h.log)
	for _, call := range h.log.calls {
		if strings.HasPrefix(call, "SetDescription:") {
			t.Errorf("интерфейс уже под намерением — повторного переименования быть не должно: %v", h.log.calls)
		}
	}
	if st := h.loadPolicyTun(t); st == nil || st.Description != "Awgmanager" || st.PendingDescription != "" {
		t.Errorf("PolicyTun persist = %+v, want Description=Awgmanager, намерение снято", st)
	}
}

// Намерение дошло до NDMS (интерфейс под B), запись не успела, и до следующего
// тика имя в настройках вернули штатное. Применённое теперь B — значит нужно
// переименование ОБРАТНО. Без сверки намерения со сканом тик счёл бы
// применённым штатное, снял намерение без переименования — и интерфейс под B
// остался бы для следующего тика «доказанно чужим»: re-provision на другом
// номере, permit'ы в политиках потеряны.
func TestReconcilePolicyTun_PendingReachedNDMS_RenamesBackToDefault(t *testing.T) {
	h := newPolicyTunEnableHarness(t, "")
	provisionPolicyTunForReconcile(t, h)
	h.svc.deps.RunningConfig = &fakeRunningConfig{lines: healthyPolicyTunRC("OpkgTun0")}
	if err := h.store.SetOpkgTunState(&storage.OpkgTunState{
		Mode: storage.OpkgTunModePolicyTun, Provisioned: true, Index: 0, PendingDescription: "B",
	}); err != nil {
		t.Fatalf("SetOpkgTunState: %v", err)
	}
	h.svc.deps.OpkgTunScan = scanOurs("B", "OpkgTun0") // NDMS: под B, под штатным — никого
	sr := setPolicyTunDescription(t, h.store, "")

	if err := h.svc.reconcilePolicyTun(context.Background(), sr); err != nil {
		t.Fatalf("reconcilePolicyTun: %v", err)
	}
	assertNoReprovision(t, h.log)
	if !h.log.has("SetDescription:OpkgTun0:" + policyTunDescription) {
		t.Errorf("интерфейс под B обязан вернуться к штатному имени: %v", h.log.calls)
	}
	if st := h.loadPolicyTun(t); st == nil || st.Index != 0 || st.Description != "" || st.PendingDescription != "" {
		t.Errorf("PolicyTun persist = %+v, want index 0, штатное описание, намерение снято", st)
	}
}

// Повтор после отказа NDMS: намерение уже на флеше с прошлого тика, и тот же
// отказ повторяется. Запись не меняется — и писаться не должна: каждый тик с
// недоступным NDMS иначе изнашивал бы флеш записью того же самого.
func TestHealPolicyTunDescription_RetryDoesNotRewriteFlash(t *testing.T) {
	h := newPolicyTunEnableHarness(t, "SetDescription")
	provisionPolicyTunForReconcile(t, h)
	st := &storage.OpkgTunState{
		Mode: storage.OpkgTunModePolicyTun, Provisioned: true, Index: 0, PendingDescription: "Awgmanager",
	}
	if err := h.store.SetOpkgTunState(st); err != nil {
		t.Fatalf("SetOpkgTunState: %v", err)
	}
	h.svc.deps.OpkgTunScan = scanOwning("OpkgTun0") // NDMS: всё ещё штатное
	sr := setPolicyTunDescription(t, h.store, "Awgmanager")
	st = h.loadPolicyTun(t)

	settingsPath := filepath.Join(h.store.DataDir(), "settings.json")
	before, err := os.Stat(settingsPath)
	if err != nil {
		t.Fatalf("stat: %v", err)
	}
	time.Sleep(10 * time.Millisecond)

	h.svc.healPolicyTunDescription(context.Background(), st, sr, "tun0", "OpkgTun0")

	if !h.log.has("SetDescription:OpkgTun0:Awgmanager") {
		t.Fatalf("повторная попытка переименования ожидалась: %v", h.log.calls)
	}
	after, err := os.Stat(settingsPath)
	if err != nil {
		t.Fatalf("stat after: %v", err)
	}
	if !after.ModTime().Equal(before.ModTime()) {
		t.Errorf("повтор с тем же намерением переписал настройки (before=%v after=%v)", before.ModTime(), after.ModTime())
	}
	if got := h.loadPolicyTun(t); got == nil || got.Description != "" || got.PendingDescription != "Awgmanager" {
		t.Errorf("PolicyTun persist = %+v, want прежнее описание и намерение Awgmanager", got)
	}
}

// Чужой интерфейс на нашем номере heal не трогает: ни переименования, ни
// правок записи. (Через reconcile сюда не дойти — чужой уходит в re-provision
// раньше; это страховка самого heal на случай другого вызывающего.)
func TestHealPolicyTunDescription_ForeignIsUntouched(t *testing.T) {
	h := newPolicyTunEnableHarness(t, "")
	provisionPolicyTunForReconcile(t, h)
	h.svc.deps.OpkgTunScan = scanNone()
	sr := setPolicyTunDescription(t, h.store, "Awgmanager")
	st := h.loadPolicyTun(t)

	h.svc.healPolicyTunDescription(context.Background(), st, sr, "tun0", "OpkgTun0")

	if len(h.log.calls) != 0 {
		t.Errorf("чужой интерфейс тронут: %v", h.log.calls)
	}
	if got := h.loadPolicyTun(t); got == nil || got.Description != "" || got.PendingDescription != "" {
		t.Errorf("PolicyTun persist = %+v, want запись нетронута", got)
	}
}

// Недоделанное выключение (tun-инбаунд пропал из слота): reconcile сбрасывает
// запись до удержания и переподнимает режим. Описания обязаны пережить сброс:
// интерфейс стоит под применённым именем, и без них переподъём не признал бы
// его своим — Create ушёл бы на другой номер, permit'ы в политиках потеряны.
func TestReconcilePolicyTun_SlotResetKeepsDescription(t *testing.T) {
	h := newPolicyTunEnableHarness(t, "")
	setPolicyTunDescription(t, h.store, "Awgmanager")
	sr := provisionPolicyTunForReconcile(t, h)
	h.svc.deps.RunningConfig = &fakeRunningConfig{lines: healthyPolicyTunRC("OpkgTun0")}
	h.svc.deps.OpkgTunScan = scanOurs("Awgmanager", "OpkgTun0")
	if st := h.loadPolicyTun(t); st == nil || st.Description != "Awgmanager" {
		t.Fatalf("фикстура: PolicyTun persist = %+v, want Description=Awgmanager", st)
	}
	// Вырезаем tun-инбаунд из применённого слота — так выглядит крах между
	// шагом 4 выключения и записью персиста.
	activePath := filepath.Join(h.dir, "20-router.json")
	raw, err := os.ReadFile(activePath)
	if err != nil {
		t.Fatalf("read active: %v", err)
	}
	var cfg map[string]any
	if err := json.Unmarshal(raw, &cfg); err != nil {
		t.Fatalf("unmarshal active: %v", err)
	}
	var kept []any
	for _, in := range cfg["inbounds"].([]any) {
		if in.(map[string]any)["tag"] != "tun-in" {
			kept = append(kept, in)
		}
	}
	cfg["inbounds"] = kept
	if raw, err = json.Marshal(cfg); err != nil {
		t.Fatalf("marshal active: %v", err)
	}
	if err := os.WriteFile(activePath, raw, 0644); err != nil {
		t.Fatalf("write active: %v", err)
	}
	probe := &createPersistProbe{OpkgTunProvisioner: h.svc.deps.OpkgTun, store: h.store}
	h.svc.deps.OpkgTun = probe

	if err := h.svc.reconcilePolicyTun(context.Background(), sr); err != nil {
		t.Fatalf("reconcilePolicyTun: %v", err)
	}
	if !h.log.has("Create:OpkgTun0:public") {
		t.Errorf("переподъём обязан переиспользовать свой номер 0: %v", h.log.calls)
	}
	if probe.atCreate == nil || probe.atCreate.Description != "Awgmanager" || probe.atCreate.PendingDescription != "" {
		t.Errorf("запись в момент Create = %+v, want Description=Awgmanager без намерения (сброс сохранил описание)", probe.atCreate)
	}
	if st := h.loadPolicyTun(t); st == nil || !st.Provisioned || st.Index != 0 || st.Description != "Awgmanager" {
		t.Errorf("PolicyTun persist = %+v, want provisioned index 0, Description=Awgmanager", st)
	}
}

// Откат enable после Create на переиспользованном номере: интерфейс уже
// переименован в желаемое, запись возвращена прежняя. Если снос в откате не
// удался, интерфейс живёт под именем, которого прежняя запись не называет, —
// поэтому откат пишет в неё намерение, и следующее включение признаёт
// интерфейс своим (желаемое из настроек в набор владения не входит).
func TestPolicyTunEnable_RollbackRecordsRenameIntention(t *testing.T) {
	h := newPolicyTunEnableHarness(t, "SetDefaultRoute") // сбой ПОСЛЕ Create
	h.svc.deps.OpkgTunIndices = &recIndices{live: map[int]bool{3: true}}
	h.svc.deps.OpkgTunScan = scanOwning("OpkgTun3") // удержанный свой, штатное описание
	if err := h.store.SetOpkgTunState(&storage.OpkgTunState{Mode: storage.OpkgTunModePolicyTun, Index: 3}); err != nil {
		t.Fatalf("SetOpkgTunState: %v", err)
	}
	setPolicyTunDescription(t, h.store, "Awgmanager")

	if err := h.svc.Enable(context.Background()); err == nil {
		t.Fatal("Enable обязан упасть на SetDefaultRoute")
	}
	if !h.log.has("Create:OpkgTun3:public") {
		t.Fatalf("удержанный свой номер 3 обязан переиспользоваться: %v", h.log.calls)
	}
	if st := h.loadPolicyTun(t); st == nil || st.Index != 3 || st.Description != "" || st.PendingDescription != "Awgmanager" {
		t.Errorf("PolicyTun persist после отката = %+v, want index 3, прежнее (штатное) описание и намерение Awgmanager", st)
	}
}

// Скан владения упал — «не знаем» ≠ «наш»: чужой интерфейс на нашем номере
// переименовывать нельзя. Запись не трогаем, повтор следующим тиком.
func TestReconcilePolicyTun_NoRenameWhenScanFails(t *testing.T) {
	h := newPolicyTunEnableHarness(t, "")
	provisionPolicyTunForReconcile(t, h)
	h.svc.deps.RunningConfig = &fakeRunningConfig{lines: healthyPolicyTunRC("OpkgTun0")}
	h.svc.deps.OpkgTunScan = func(context.Context, string) ([]string, error) {
		return nil, errors.New("injected: scan")
	}
	sr := setPolicyTunDescription(t, h.store, "Awgmanager")

	if err := h.svc.reconcilePolicyTun(context.Background(), sr); err != nil {
		t.Fatalf("reconcilePolicyTun: %v", err)
	}
	for _, c := range h.log.calls {
		if strings.HasPrefix(c, "SetDescription:") {
			t.Fatalf("без доказанного владения переименования быть не должно: %v", h.log.calls)
		}
	}
	assertNoReprovision(t, h.log)
	if st := h.loadPolicyTun(t); st == nil || st.Description != "" {
		t.Errorf("PolicyTun persist = %+v, want прежнее описание", st)
	}
}

// Отказ NDMS на переименовании — применённое в записи остаётся прежним (оно
// по-прежнему называет то, что на интерфейсе), а намерение уже записано:
// оно легло на флеш ДО обращения к NDMS. Режим не пересоздаётся.
func TestReconcilePolicyTun_RenameFailureKeepsRecord(t *testing.T) {
	h := newPolicyTunEnableHarness(t, "SetDescription")
	provisionPolicyTunForReconcile(t, h)
	h.svc.deps.RunningConfig = &fakeRunningConfig{lines: healthyPolicyTunRC("OpkgTun0")}
	h.svc.deps.OpkgTunScan = scanOwning("OpkgTun0")
	sr := setPolicyTunDescription(t, h.store, "Awgmanager")

	if err := h.svc.reconcilePolicyTun(context.Background(), sr); err != nil {
		t.Fatalf("reconcilePolicyTun: %v", err)
	}
	if !h.log.has("SetDescription:OpkgTun0:Awgmanager") {
		t.Fatalf("попытка переименования ожидалась: %v", h.log.calls)
	}
	assertNoReprovision(t, h.log)
	if st := h.loadPolicyTun(t); st == nil || st.Description != "" || st.PendingDescription != "Awgmanager" {
		t.Errorf("PolicyTun persist = %+v, want прежнее описание и намерение Awgmanager", st)
	}
}

// Реап сирот по описанию НЕ идёт по пользовательскому имени: оно не
// уникально, и под ним может жить чужой OpkgTun (например, туннель с тем же
// названием, OpkgTun4). Свой интерфейс вне режима снимается по записи
// владения, как и прежде, — и только он.
func TestPolicyTunReap_SparesForeignInterfaceWithSameName(t *testing.T) {
	cases := []struct {
		mode        string
		wantDeleted []string
	}{
		{mode: statePolicyTun, wantDeleted: nil},            // режим владеет своим
		{mode: "tproxy", wantDeleted: []string{"OpkgTun0"}}, // персист-реап своего
	}
	for _, c := range cases {
		t.Run(c.mode, func(t *testing.T) {
			store := newTestSettingsStore(t, storage.SingboxRouterSettings{
				RoutingMode: c.mode, PolicyTunDescription: "Polan",
			})
			if err := store.SetOpkgTunState(&storage.OpkgTunState{
				Mode: storage.OpkgTunModePolicyTun, Provisioned: true, Index: 0, Description: "Polan",
			}); err != nil {
				t.Fatalf("SetOpkgTunState: %v", err)
			}
			opkg := &recordingOpkgTunProvisioner{}
			scan := &recOpkgTunScan{ids: map[string][]string{"Polan": {"OpkgTun0", "OpkgTun4"}}}
			svc := newTestService(t, Deps{Settings: store, OpkgTun: opkg, OpkgTunScan: scan.scan})

			if err := svc.ReapOrphanedFakeIPTun(context.Background()); err != nil {
				t.Fatalf("ReapOrphanedFakeIPTun: %v", err)
			}
			if !reflect.DeepEqual(opkg.deleted, c.wantDeleted) {
				t.Errorf("deleted = %v, want %v", opkg.deleted, c.wantDeleted)
			}
		})
	}
}

// Удаление пакета снимает переименованный интерфейс: владение признаётся по
// применённому имени из записи.
func TestReleasePolicyTunForRemoval_CustomDescription(t *testing.T) {
	stubLinkAbsent(t)
	store := newTestSettingsStore(t, storage.SingboxRouterSettings{
		RoutingMode: statePolicyTun, PolicyTunDescription: "Awgmanager",
	})
	if err := store.SetOpkgTunState(&storage.OpkgTunState{
		Mode: storage.OpkgTunModePolicyTun, Index: 2, Description: "Awgmanager",
	}); err != nil {
		t.Fatalf("SetOpkgTunState: %v", err)
	}
	opkg := &recordingOpkgTunProvisioner{}
	scan := &recOpkgTunScan{ids: map[string][]string{"Awgmanager": {"OpkgTun2"}}}

	if err := ReleasePolicyTunForRemoval(context.Background(), Deps{
		SwapGate:    &netdev.SwapGate{},
		Settings:    store,
		OpkgTun:     opkg,
		OpkgTunScan: scan.scan,
	}); err != nil {
		t.Fatalf("ReleasePolicyTunForRemoval: %v", err)
	}
	if len(opkg.deleted) != 1 || opkg.deleted[0] != "OpkgTun2" {
		t.Errorf("deleted = %v, want [OpkgTun2]", opkg.deleted)
	}
}

// Выключение удерживает переименованный интерфейс как свой: под штатным
// описанием его нет, но запись помнит применённое.
func TestPolicyTunDisable_HoldsRenamedInterface(t *testing.T) {
	h := newPolicyTunEnableHarness(t, "")
	setPolicyTunDescription(t, h.store, "Awgmanager")
	provisionPolicyTunForDisable(t, h)
	h.svc.deps.OpkgTunScan = scanOurs("Awgmanager", "OpkgTun0")

	if err := h.svc.Disable(context.Background()); err != nil {
		t.Fatalf("Disable(policy-tun): %v", err)
	}
	if !h.log.has("RemoveDefaultRoute:OpkgTun0") {
		t.Errorf("свой интерфейс обязан разбираться (дефолт снимается): %v", h.log.calls)
	}
	if st := h.loadPolicyTun(t); st == nil || st.Index != 0 || st.Description != "Awgmanager" {
		t.Errorf("PolicyTun persist = %+v, want удержание index 0 с Description=Awgmanager", st)
	}
}
