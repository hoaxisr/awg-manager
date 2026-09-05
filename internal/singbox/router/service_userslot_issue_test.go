package router

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/hoaxisr/awg-manager/internal/singbox/orchestrator"
	"github.com/hoaxisr/awg-manager/internal/storage"
)

// newUserSlotOrch строит оркестратор с включённым user-слотом, чей
// применённый файл ссылается на несуществующий тег, и прогоняет Reload —
// reload пропускается, провал оседает в LastReloadValidation.
func newUserSlotOrch(t *testing.T, userJSON string) *orchestrator.Orchestrator {
	t.Helper()
	dir := t.TempDir()
	o := orchestrator.NewWithAppliedPath(dir, nil, filepath.Join(t.TempDir(), "singbox-applied.json"))
	for _, meta := range orchestrator.KnownSlots() {
		if meta.Slot == orchestrator.SlotUser {
			if err := o.Register(meta); err != nil {
				t.Fatalf("register: %v", err)
			}
		}
	}
	if err := o.Bootstrap(); err != nil {
		t.Fatalf("bootstrap: %v", err)
	}
	if err := os.WriteFile(filepath.Join(dir, "90-user.json"), []byte(userJSON), 0644); err != nil {
		t.Fatal(err)
	}
	if err := o.SetEnabledSilent(orchestrator.SlotUser, true); err != nil {
		t.Fatalf("enable user slot: %v", err)
	}
	_ = o.Reload() // валидация провалится — это и есть фикстура
	return o
}

// Провал reload-валидации по вине 90-user.json всплывает в статусе роутера
// как issue user-slot-validation (правки пользователя не чиним молча —
// prune скипает user-слот, поэтому это единственная поверхность).
func TestGetStatus_SurfacesUserSlotReloadValidation(t *testing.T) {
	stubListeningProbe(t, func() bool { return false })
	settingsStore := newTestSettingsStore(t, storage.SingboxRouterSettings{
		Enabled:    false,
		PolicyName: "Policy0",
	})
	orch := newUserSlotOrch(t, `{"route":{"rules":[{"outbound":"ghost-tag"}]}}`)
	if orch.LastReloadValidation() == nil {
		t.Fatal("fixture: LastReloadValidation must be non-nil after failed reload")
	}

	fe := &fakeExec{}
	svc := newTestService(t, Deps{
		Settings: settingsStore,
		Policies: &fakeAccessPolicyProvider{mark: "0xffffaaa"},
		IPTables: newTestIPTables(fe),
		Singbox:  newTestSingbox(t),
		Orch:     orch,
	})
	if err := SaveConfig(svc.routerConfigPath(), NewEmptyConfig()); err != nil {
		t.Fatal(err)
	}

	st, err := svc.GetStatus(context.Background())
	if err != nil {
		t.Fatalf("GetStatus: %v", err)
	}
	var hit *Issue
	for i := range st.Issues {
		if st.Issues[i].Kind == "user-slot-validation" {
			hit = &st.Issues[i]
		}
	}
	if hit == nil {
		t.Fatalf("user-slot-validation issue missing: %+v", st.Issues)
	}
	if hit.Severity != "error" || hit.Tag != "ghost-tag" {
		t.Errorf("issue shape: %+v", hit)
	}
	if !strings.Contains(hit.Message, "90-user.json") ||
		!strings.Contains(hit.Message, `"ghost-tag"`) ||
		!strings.Contains(hit.Message, "редакторе") {
		t.Errorf("issue message: %q", hit.Message)
	}
}

// После починки user-слота и успешного reload issue исчезает.
func TestGetStatus_UserSlotIssueClearedAfterFix(t *testing.T) {
	stubListeningProbe(t, func() bool { return false })
	settingsStore := newTestSettingsStore(t, storage.SingboxRouterSettings{
		Enabled:    false,
		PolicyName: "Policy0",
	})
	orch := newUserSlotOrch(t, `{"route":{"rules":[{"outbound":"ghost-tag"}]}}`)

	// Чиним слот и перезапускаем reload — LastReloadValidation очищается.
	if err := os.WriteFile(filepath.Join(orch.ConfigDir(), "90-user.json"), []byte(`{}`), 0644); err != nil {
		t.Fatal(err)
	}
	if err := orch.Reload(); err != nil {
		t.Fatalf("reload after fix: %v", err)
	}

	fe := &fakeExec{}
	svc := newTestService(t, Deps{
		Settings: settingsStore,
		Policies: &fakeAccessPolicyProvider{mark: "0xffffaaa"},
		IPTables: newTestIPTables(fe),
		Singbox:  newTestSingbox(t),
		Orch:     orch,
	})
	if err := SaveConfig(svc.routerConfigPath(), NewEmptyConfig()); err != nil {
		t.Fatal(err)
	}

	st, err := svc.GetStatus(context.Background())
	if err != nil {
		t.Fatalf("GetStatus: %v", err)
	}
	for _, is := range st.Issues {
		if is.Kind == "user-slot-validation" {
			t.Errorf("stale user-slot-validation issue: %+v", is)
		}
	}
}
