package service

import (
	"context"
	"errors"
	"reflect"
	"strings"
	"testing"

	"github.com/hoaxisr/awg-manager/internal/storage"
	"github.com/hoaxisr/awg-manager/internal/tunnel"
)

// Имя туннеля — описание его записи OpkgTun в NDMS, а NDMS принимает не
// больше 256 байт (на 257 заводит запись с ПУСТЫМ описанием и отвечает
// ошибкой — и F517 затем не признаёт запись нашей). Все пути, ставящие имя —
// импорт, правка карточки, замена конфигурации, — упираются в один предел.
var (
	name256 = strings.Repeat("ж", 128)       // 256 байт
	name257 = strings.Repeat("ж", 128) + "a" // 257 байт
)

func wantNameTooLong(t *testing.T, err error) {
	t.Helper()
	if !errors.Is(err, tunnel.ErrNameTooLong) {
		t.Fatalf("err = %v, want ErrNameTooLong", err)
	}
}

func TestImport_NameByteLimit(t *testing.T) {
	s, _, _ := serviceForImport(t)
	if _, err := s.Import(context.Background(), sampleConf, name256, "kernel", ImportLink{}); err != nil {
		t.Fatalf("Import(256 байт): %v", err)
	}

	s, _, _ = serviceForImport(t)
	_, err := s.Import(context.Background(), sampleConf, name257, "kernel", ImportLink{})
	wantNameTooLong(t, err)
	if list, _ := s.store.List(); len(list) != 0 {
		t.Fatalf("запись заведена при отказе: %d", len(list))
	}
}

func TestUpdate_RenameByteLimit(t *testing.T) {
	stored := func(name string) *storage.AWGTunnel {
		return &storage.AWGTunnel{ID: "awg10", Backend: "kernel", Name: name,
			Interface: storage.AWGInterface{Address: "10.0.0.1/32", MTU: 1420}}
	}
	newSvc := func() (*ServiceImpl, *MockOperator) {
		op := &MockOperator{}
		return &ServiceImpl{legacyOperator: op, state: NewMockStateManager()}, op
	}

	s, _ := newSvc()
	if err := s.Update(context.Background(), stored("Germany"), stored(name256)); err != nil {
		t.Fatalf("rename в 256 байт: %v", err)
	}

	s, op := newSvc()
	wantNameTooLong(t, s.Update(context.Background(), stored("Germany"), stored(name257)))
	if len(op.UpdateDescriptionCalls) != 0 {
		t.Fatalf("описание в NDMS ушло при отказе: %v", op.UpdateDescriptionCalls)
	}

	// Имя не меняли — правка других полей не отвергается из-за имени,
	// заведённого до предела.
	s, _ = newSvc()
	if err := s.Update(context.Background(), stored(name257), stored(name257)); err != nil {
		t.Fatalf("правка без переименования: %v", err)
	}
}

func TestReplaceConfig_RenameByteLimit(t *testing.T) {
	for _, tc := range []struct {
		name    string
		newName string
		ok      bool
	}{{"256 байт", name256, true}, {"257 байт", name257, false}} {
		t.Run(tc.name, func(t *testing.T) {
			s, _ := serviceWithStore(t)
			if err := s.store.Create(&storage.AWGTunnel{ID: "awg10", Name: "Germany", Backend: "kernel"}); err != nil {
				t.Fatal(err)
			}
			err := s.ReplaceConfig(context.Background(), "awg10", sampleConf, tc.newName, ReplaceOptions{})
			got, _ := s.store.Get("awg10")
			if tc.ok {
				if err != nil || got.Name != tc.newName {
					t.Fatalf("ReplaceConfig: err=%v name=%q", err, got.Name)
				}
				return
			}
			wantNameTooLong(t, err)
			if got.Name != "Germany" {
				t.Fatalf("имя сменилось при отказе: %q", got.Name)
			}
		})
	}
}

// F517 признаёт запись OpkgTun своей по равенству её описания имени туннеля.
// Замена конфигурации с новым именем у kernel-туннеля обязана переписать
// описание — раньше это делалось только для nativewg, и после ребута
// переименованный туннель не стартовал бы. Без нового имени NDMS не трогается.
func TestReplaceConfig_KernelRenameSyncsDescription(t *testing.T) {
	for _, tc := range []struct {
		name, newName string
		want          []struct{ ID, Desc string }
	}{
		{"новое имя", "Norway", []struct{ ID, Desc string }{{"awg10", "Norway"}}},
		{"имя не меняли", "", nil},
	} {
		t.Run(tc.name, func(t *testing.T) {
			s, _ := serviceWithStore(t)
			op := &MockOperator{}
			s.legacyOperator = op
			if err := s.store.Create(&storage.AWGTunnel{ID: "awg10", Name: "Germany", Backend: "kernel"}); err != nil {
				t.Fatal(err)
			}
			if err := s.ReplaceConfig(context.Background(), "awg10", sampleConf, tc.newName, ReplaceOptions{}); err != nil {
				t.Fatalf("ReplaceConfig: %v", err)
			}
			if !reflect.DeepEqual(op.UpdateDescriptionCalls, tc.want) {
				t.Fatalf("UpdateDescription = %+v, want %+v", op.UpdateDescriptionCalls, tc.want)
			}
		})
	}
}

// SyncDescription — вход для путей, меняющих имя мимо Update (взятие
// стороннего туннеля, переименование волной wdttlink): kernel-туннель — через
// legacy-оператор, с именем, которое передали.
func TestSyncDescription_Kernel(t *testing.T) {
	s, _ := serviceWithStore(t)
	op := &MockOperator{}
	s.legacyOperator = op
	if err := s.store.Create(&storage.AWGTunnel{ID: "awg10", Name: "Germany", Backend: "kernel"}); err != nil {
		t.Fatal(err)
	}
	s.SyncDescription(context.Background(), "awg10", "Germany wdtt")
	want := []struct{ ID, Desc string }{{"awg10", "Germany wdtt"}}
	if !reflect.DeepEqual(op.UpdateDescriptionCalls, want) {
		t.Fatalf("UpdateDescription = %+v, want %+v", op.UpdateDescriptionCalls, want)
	}
}
