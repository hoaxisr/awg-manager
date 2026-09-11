package subscription

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/hoaxisr/awg-manager/internal/singbox/orchestrator"
)

// slotPath — путь, по которому LoadFromDisk ищет слот. Совпадение с тем,
// куда его кладёт оркестратор, тест проверяет явно: разойдись они, адаптер
// стартовал бы с пустой памятью при живом файле на диске.
func slotPath(dir string) string { return filepath.Join(dir, "40-subscriptions.json") }

func newTestAdapter(t *testing.T, dir string) *OperatorAdapter {
	t.Helper()
	orch := orchestrator.NewWithAppliedPath(dir, nil, filepath.Join(t.TempDir(), "singbox-applied.json"))
	t.Cleanup(orch.Close)
	if err := orch.Bootstrap(); err != nil {
		t.Fatal(err)
	}
	orch.SetValidator(&countingValidator{})
	return NewOperatorAdapter(orch, nil, nil)
}

// F202: слот на диске есть, но прочитать его не вышло. flush пишет слот
// ЦЕЛИКОМ из памяти, поэтому запись из памяти, не сверенной с диском, стёрла
// бы все подписки, кроме той, над которой шла операция, — а операцию заводит
// планировщик автообновления сам. Записи быть не должно.
func TestLoadFromDisk_UnreadableSlotBlocksWrites(t *testing.T) {
	dir := t.TempDir()
	// Каталог вместо файла: ReadFile отдаёт ошибку, отличную от «нет файла»,
	// без возни с правами и без root.
	if err := os.Mkdir(slotPath(dir), 0o755); err != nil {
		t.Fatal(err)
	}
	adapter := newTestAdapter(t, dir)

	if err := adapter.LoadFromDisk(dir); err == nil {
		t.Fatal("LoadFromDisk на нечитаемом слоте обязан вернуть ошибку")
	}
	if err := adapter.AddOutbound("sub-x-0", validVlessJSON("10.0.0.1")); err != nil {
		t.Fatal(err)
	}
	err := adapter.Reload(context.Background())
	if err == nil {
		t.Fatal("коммит при непрочитанном слоте обязан отказать")
	}
	if !strings.Contains(err.Error(), "не прочитан при старте") {
		t.Fatalf("отказ не про непрочитанный слот: %v", err)
	}
	// Каталог на месте — оркестратор в него не писал.
	if fi, statErr := os.Stat(slotPath(dir)); statErr != nil || !fi.IsDir() {
		t.Fatalf("слот на диске тронут: %v", statErr)
	}
}

// Битый JSON — другой случай: такой файл не переварит и сам sing-box, значит
// подписки уже не работают. Файл уходит в карантин, а работа продолжается,
// иначе подписки залипли бы до ручного вмешательства на роутере.
func TestLoadFromDisk_CorruptSlotQuarantinedAndWritesResume(t *testing.T) {
	dir := t.TempDir()
	adapter := newTestAdapter(t, dir)

	// Сначала убеждаемся, что оркестратор пишет туда же, откуда читает
	// LoadFromDisk, — иначе весь сценарий ниже проверял бы не тот файл.
	if err := adapter.AddOutbound("sub-x-0", validVlessJSON("10.0.0.1")); err != nil {
		t.Fatal(err)
	}
	if err := adapter.Reload(context.Background()); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(slotPath(dir)); err != nil {
		t.Fatalf("оркестратор пишет слот не туда, где его ищет LoadFromDisk: %v", err)
	}

	if err := os.WriteFile(slotPath(dir), []byte("{не json"), 0o644); err != nil {
		t.Fatal(err)
	}
	fresh := newTestAdapter(t, dir)
	if err := fresh.LoadFromDisk(dir); err == nil {
		t.Fatal("битый слот обязан вернуть ошибку разбора")
	}
	if _, err := os.Stat(slotPath(dir) + ".corrupt"); err != nil {
		t.Fatalf("битый слот не унесён в карантин: %v", err)
	}
	if _, err := os.Stat(slotPath(dir)); !os.IsNotExist(err) {
		t.Fatalf("битый слот остался на месте: %v", err)
	}
	// Работа продолжается: запись не заблокирована.
	if err := fresh.AddOutbound("sub-y-0", validVlessJSON("10.0.0.2")); err != nil {
		t.Fatal(err)
	}
	if err := fresh.Reload(context.Background()); err != nil {
		t.Fatalf("после карантина запись обязана идти: %v", err)
	}
	if got := fresh.DeclaredOutboundTags(); len(got) != 1 || got[0] != "sub-y-0" {
		t.Fatalf("слот после карантина = %v", got)
	}
}
