package router

import (
	"context"
	"errors"
	"reflect"
	"strings"
	"testing"

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
		{in: "AWGManager", want: "AWGManager"},
		{in: "  AWGManager  ", want: "AWGManager"},
		// Штатное хранится пустым: одно значение — одно представление.
		{in: "awgm policy-tun", want: ""},
		{in: " awgm policy-tun ", want: ""},
		{in: "Мой туннель", want: "Мой туннель"},
		{in: strings.Repeat("я", policyTunDescriptionMaxRunes), want: strings.Repeat("я", policyTunDescriptionMaxRunes)},
		{in: strings.Repeat("a", policyTunDescriptionMaxRunes+1), wantErr: true},
		{in: "a\tb", wantErr: true},
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

func TestPolicyTunOwnDescriptions(t *testing.T) {
	custom := func(d string) storage.SingboxRouterSettings {
		return storage.SingboxRouterSettings{PolicyTunDescription: d}
	}
	rec := func(d string) *storage.OpkgTunState {
		return &storage.OpkgTunState{Mode: storage.OpkgTunModePolicyTun, Description: d}
	}
	cases := []struct {
		name string
		st   *storage.OpkgTunState
		sr   storage.SingboxRouterSettings
		want []string
	}{
		{"нет записи, дефолт", nil, custom(""), []string{policyTunDescription}},
		{"запись без описания (старая версия)", rec(""), custom(""), []string{policyTunDescription}},
		{"переименование из дефолта", rec(""), custom("AWGManager"), []string{policyTunDescription, "AWGManager"}},
		{"применено", rec("AWGManager"), custom("AWGManager"), []string{"AWGManager"}},
		{"возврат к дефолту", rec("AWGManager"), custom(""), []string{"AWGManager", policyTunDescription}},
	}
	for _, c := range cases {
		if got := policyTunOwnDescriptions(c.st, c.sr); !reflect.DeepEqual(got, c.want) {
			t.Errorf("%s: %v, want %v", c.name, got, c.want)
		}
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

// Новый интерфейс получает имя из настроек, и запись владения помнит его уже к
// моменту Create (persist-before-create).
func TestPolicyTunEnable_CustomDescription(t *testing.T) {
	h := newPolicyTunEnableHarness(t, "")
	setPolicyTunDescription(t, h.store, "AWGManager")
	probe := &createPersistProbe{OpkgTunProvisioner: h.svc.deps.OpkgTun, store: h.store}
	h.svc.deps.OpkgTun = probe

	if err := h.svc.Enable(context.Background()); err != nil {
		t.Fatalf("Enable(policy-tun): %v", err)
	}
	if len(h.opkg.descs) != 1 || h.opkg.descs[0] != "AWGManager" {
		t.Errorf("Create description = %v, want [AWGManager]", h.opkg.descs)
	}
	if probe.atCreate == nil || probe.atCreate.Description != "AWGManager" {
		t.Errorf("запись в момент Create = %+v, want Description=AWGManager", probe.atCreate)
	}
	if st := h.loadPolicyTun(t); st == nil || st.Description != "AWGManager" {
		t.Errorf("PolicyTun persist = %+v, want Description=AWGManager", st)
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
	setPolicyTunDescription(t, h.store, "AWGManager")
	probe := &createPersistProbe{OpkgTunProvisioner: h.svc.deps.OpkgTun, store: h.store}
	h.svc.deps.OpkgTun = probe

	if err := h.svc.Enable(context.Background()); err != nil {
		t.Fatalf("Enable(policy-tun): %v", err)
	}
	if !h.log.has("Create:OpkgTun3:public") {
		t.Fatalf("удержанный свой индекс 3 обязан переиспользоваться: %v", h.log.calls)
	}
	if probe.atCreate == nil || probe.atCreate.Description != "" {
		t.Errorf("запись в момент Create = %+v, want прежнее (штатное) описание", probe.atCreate)
	}
	if st := h.loadPolicyTun(t); st == nil || st.Index != 3 || st.Description != "AWGManager" {
		t.Errorf("PolicyTun persist = %+v, want index 3, Description=AWGManager", st)
	}
}

// Переименование живого интерфейса — это SetDescription на тике reconcile, а
// не пересоздание: permit'ы в политиках привязаны к объекту.
func TestReconcilePolicyTun_RenamesLiveInterface(t *testing.T) {
	h := newPolicyTunEnableHarness(t, "")
	provisionPolicyTunForReconcile(t, h)
	h.svc.deps.RunningConfig = &fakeRunningConfig{lines: healthyPolicyTunRC("OpkgTun0")}
	h.svc.deps.OpkgTunScan = scanOwning("OpkgTun0") // NDMS: пока штатное описание
	sr := setPolicyTunDescription(t, h.store, "AWGManager")

	if err := h.svc.reconcilePolicyTun(context.Background(), sr); err != nil {
		t.Fatalf("reconcilePolicyTun: %v", err)
	}
	if !h.log.has("SetDescription:OpkgTun0:AWGManager") {
		t.Errorf("живой интерфейс обязан переименоваться: %v", h.log.calls)
	}
	assertNoReprovision(t, h.log)
	if st := h.loadPolicyTun(t); st == nil || !st.Provisioned || st.Index != 0 || st.Description != "AWGManager" {
		t.Errorf("PolicyTun persist = %+v, want provisioned index 0, Description=AWGManager", st)
	}

	// Следующий тик: NDMS уже под новым именем — ни одной мутации.
	h.svc.deps.OpkgTunScan = scanOurs("AWGManager", "OpkgTun0")
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
	setPolicyTunDescription(t, h.store, "AWGManager")
	provisionPolicyTunForReconcile(t, h)
	h.svc.deps.RunningConfig = &fakeRunningConfig{lines: healthyPolicyTunRC("OpkgTun0")}
	h.svc.deps.OpkgTunScan = scanOurs("AWGManager", "OpkgTun0")
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

// Крах между переименованием в NDMS и записью: интерфейс уже под новым
// именем, запись — под прежним. Владение признаёт желаемое имя, поэтому тик
// дописывает запись, а не провижинит режим заново поверх живого интерфейса.
func TestReconcilePolicyTun_RenamedButNotPersisted(t *testing.T) {
	h := newPolicyTunEnableHarness(t, "")
	provisionPolicyTunForReconcile(t, h)
	h.svc.deps.RunningConfig = &fakeRunningConfig{lines: healthyPolicyTunRC("OpkgTun0")}
	h.svc.deps.OpkgTunScan = scanOurs("AWGManager", "OpkgTun0") // NDMS уже переименован
	sr := setPolicyTunDescription(t, h.store, "AWGManager")

	if err := h.svc.reconcilePolicyTun(context.Background(), sr); err != nil {
		t.Fatalf("reconcilePolicyTun: %v", err)
	}
	assertNoReprovision(t, h.log)
	if st := h.loadPolicyTun(t); st == nil || st.Description != "AWGManager" {
		t.Errorf("PolicyTun persist = %+v, want Description=AWGManager", st)
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
	sr := setPolicyTunDescription(t, h.store, "AWGManager")

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

// Отказ NDMS на переименовании — запись остаётся прежней (она по-прежнему
// называет то, что на интерфейсе), режим не пересоздаётся.
func TestReconcilePolicyTun_RenameFailureKeepsRecord(t *testing.T) {
	h := newPolicyTunEnableHarness(t, "SetDescription")
	provisionPolicyTunForReconcile(t, h)
	h.svc.deps.RunningConfig = &fakeRunningConfig{lines: healthyPolicyTunRC("OpkgTun0")}
	h.svc.deps.OpkgTunScan = scanOwning("OpkgTun0")
	sr := setPolicyTunDescription(t, h.store, "AWGManager")

	if err := h.svc.reconcilePolicyTun(context.Background(), sr); err != nil {
		t.Fatalf("reconcilePolicyTun: %v", err)
	}
	if !h.log.has("SetDescription:OpkgTun0:AWGManager") {
		t.Fatalf("попытка переименования ожидалась: %v", h.log.calls)
	}
	assertNoReprovision(t, h.log)
	if st := h.loadPolicyTun(t); st == nil || st.Description != "" {
		t.Errorf("PolicyTun persist = %+v, want прежнее описание", st)
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
		RoutingMode: statePolicyTun, PolicyTunDescription: "AWGManager",
	})
	if err := store.SetOpkgTunState(&storage.OpkgTunState{
		Mode: storage.OpkgTunModePolicyTun, Index: 2, Description: "AWGManager",
	}); err != nil {
		t.Fatalf("SetOpkgTunState: %v", err)
	}
	opkg := &recordingOpkgTunProvisioner{}
	scan := &recOpkgTunScan{ids: map[string][]string{"AWGManager": {"OpkgTun2"}}}

	if err := ReleasePolicyTunForRemoval(context.Background(), Deps{
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
	setPolicyTunDescription(t, h.store, "AWGManager")
	provisionPolicyTunForDisable(t, h)
	h.svc.deps.OpkgTunScan = scanOurs("AWGManager", "OpkgTun0")

	if err := h.svc.Disable(context.Background()); err != nil {
		t.Fatalf("Disable(policy-tun): %v", err)
	}
	if !h.log.has("RemoveDefaultRoute:OpkgTun0") {
		t.Errorf("свой интерфейс обязан разбираться (дефолт снимается): %v", h.log.calls)
	}
	if st := h.loadPolicyTun(t); st == nil || st.Index != 0 || st.Description != "AWGManager" {
		t.Errorf("PolicyTun persist = %+v, want удержание index 0 с Description=AWGManager", st)
	}
}
