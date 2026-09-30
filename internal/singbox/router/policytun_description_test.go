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
		{in: "Awgmanager", want: "Awgmanager"},
		{in: "  Awgmanager  ", want: "Awgmanager"},
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
		{"переименование из дефолта", rec(""), custom("Awgmanager"), []string{policyTunDescription, "Awgmanager"}},
		{"применено", rec("Awgmanager"), custom("Awgmanager"), []string{"Awgmanager"}},
		{"возврат к дефолту", rec("Awgmanager"), custom(""), []string{"Awgmanager", policyTunDescription}},
		// Намерение — третье имя: настройку сменили, пока переименование в B
		// не подтверждено записью.
		{"намерение и новая настройка", pendingRec("", "B"), custom("C"), []string{policyTunDescription, "B", "C"}},
		{"намерение совпадает с настройкой", pendingRec("", "B"), custom("B"), []string{policyTunDescription, "B"}},
		{"намерение — штатное имя", pendingRec("Awgmanager", policyTunDescription), custom(""), []string{"Awgmanager", policyTunDescription}},
	}
	for _, c := range cases {
		if got := policyTunOwnDescriptions(c.st, c.sr); !reflect.DeepEqual(got, c.want) {
			t.Errorf("%s: %v, want %v", c.name, got, c.want)
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

// Крах между переименованием в NDMS и записью: интерфейс уже под новым
// именем, запись — под прежним. Владение признаёт желаемое имя, поэтому тик
// дописывает запись, а не провижинит режим заново поверх живого интерфейса.
func TestReconcilePolicyTun_RenamedButNotPersisted(t *testing.T) {
	h := newPolicyTunEnableHarness(t, "")
	provisionPolicyTunForReconcile(t, h)
	h.svc.deps.RunningConfig = &fakeRunningConfig{lines: healthyPolicyTunRC("OpkgTun0")}
	h.svc.deps.OpkgTunScan = scanOurs("Awgmanager", "OpkgTun0") // NDMS уже переименован
	sr := setPolicyTunDescription(t, h.store, "Awgmanager")

	if err := h.svc.reconcilePolicyTun(context.Background(), sr); err != nil {
		t.Fatalf("reconcilePolicyTun: %v", err)
	}
	assertNoReprovision(t, h.log)
	if st := h.loadPolicyTun(t); st == nil || st.Description != "Awgmanager" {
		t.Errorf("PolicyTun persist = %+v, want Description=Awgmanager", st)
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
