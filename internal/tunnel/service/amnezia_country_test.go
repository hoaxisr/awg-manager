package service

import (
	"bytes"
	"context"
	"encoding/binary"
	"strings"
	"syscall"
	"testing"

	"github.com/hoaxisr/awg-manager/internal/storage"
)

// Фикстуры намеренно различны и не совпадают ни с нулём, ни между собой:
// одинаковая страна «до» и «после» сделала бы проверку очистки слепой, а
// уже нормализованный ввод — проверку нормализации.
const (
	countryRawImport  = "  SE  " // ввод мастера при импорте; нормализация его меняет
	countryPrevStored = "pt"     // что лежало в записи ДО замены конфигурации
	countryRawWizard  = " DE "   // ввод мастера при замене конфигурации
	tunnelNameFixture = "Португалия"
)

func TestImport_StoresNormalizedAmneziaCountry(t *testing.T) {
	s, _, _ := serviceForImport(t)

	if _, err := s.Import(context.Background(), sampleConf, "Швеция", "kernel",
		ImportLink{AmneziaCountry: countryRawImport}); err != nil {
		t.Fatalf("Import: %v", err)
	}

	got := onlyStored(t, s)
	if got.AmneziaCountry != "se" {
		t.Fatalf("amneziaCountry = %q, want %q: код сравнивается со списком стран каталога строкой, %q ни с чем не совпадёт",
			got.AmneziaCountry, "se", countryRawImport)
	}
}

func TestImport_WithoutCountryLeavesFieldEmpty(t *testing.T) {
	s, _, _ := serviceForImport(t)

	if _, err := s.Import(context.Background(), sampleConf, "Файл", "kernel", ImportLink{}); err != nil {
		t.Fatalf("Import: %v", err)
	}

	got := onlyStored(t, s)
	if got.AmneziaCountry != "" {
		t.Fatalf("amneziaCountry = %q, want пусто: импорт файла к подписке отношения не имеет", got.AmneziaCountry)
	}
}

// Семантика опции страны при замене конфигурации. Ветка «очистить» — не
// формальность: пользователь, заменивший конфигурацию своим файлом, иначе
// видел бы в мастере метку «этой стране уже соответствует туннель» на
// туннеле, к подписке уже не относящемся.
func TestReplaceConfig_AmneziaCountryOption(t *testing.T) {
	empty := ""
	wizard := countryRawWizard
	cases := []struct {
		name string
		opts ReplaceOptions
		want string
	}{
		{"nil — страну не трогать", ReplaceOptions{}, countryPrevStored},
		{"пусто — замена не из мастера, метку снять", ReplaceOptions{AmneziaCountry: &empty}, ""},
		{"значение — замена из мастера", ReplaceOptions{AmneziaCountry: &wizard}, "de"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			s, _ := serviceWithStore(t)
			if err := s.store.Create(&storage.AWGTunnel{
				ID: "awg10", Name: tunnelNameFixture, Backend: "kernel",
				AmneziaCountry: countryPrevStored,
			}); err != nil {
				t.Fatal(err)
			}

			if err := s.ReplaceConfig(context.Background(), "awg10", sampleConf, "", tc.opts); err != nil {
				t.Fatalf("ReplaceConfig: %v", err)
			}

			got, err := s.store.Get("awg10")
			if err != nil {
				t.Fatalf("Get: %v", err)
			}
			if got.AmneziaCountry != tc.want {
				t.Errorf("amneziaCountry = %q, want %q", got.AmneziaCountry, tc.want)
			}
			// Пустое имя означает «имя не менять» — существующее соглашение
			// ReplaceConfig, и страна его не отменяет.
			if got.Name != tunnelNameFixture {
				t.Errorf("имя туннеля = %q, want %q: newName пуст — имя менять нечем", got.Name, tunnelNameFixture)
			}
		})
	}
}

// Страна и конфигурация ложатся в запись ОДНИМ мутатором, то есть одной
// записью файла. Вторым Update это был бы лишний цикл флеша на каждой замене
// (цель — роутер с флеш-памятью) и окно, в котором конфигурация уже новая, а
// метка страны ещё от прежней подписки.
//
// Счётчик не статистический: inotify фиксирует каждую операцию в каталоге
// записей. Считаются ПОЯВЛЕНИЯ временного файла (storage.AtomicWrite пишет
// <запись>.tmp.<pid>.<наносекунды> и переименовывает его в запись) — ровно
// одно на запись. Само имя записи для счёта не годится: два одинаковых
// подряд идущих IN_MOVED_TO ядро склеивает в одно событие, пока очередь не
// вычитана, и две записи выглядели бы как одна. Тест линуксовый по
// построению, как и проект.
func TestReplaceConfig_CountryAndConfigLandInOneStoreWrite(t *testing.T) {
	s, tunnels, _ := serviceForImport(t)
	if err := s.store.Create(&storage.AWGTunnel{
		ID: "awg10", Name: tunnelNameFixture, Backend: "kernel",
		AmneziaCountry: countryPrevStored,
	}); err != nil {
		t.Fatal(err)
	}

	fd, err := syscall.InotifyInit1(syscall.IN_NONBLOCK | syscall.IN_CLOEXEC)
	if err != nil {
		t.Fatalf("inotify_init1: %v", err)
	}
	defer syscall.Close(fd)
	if _, err := syscall.InotifyAddWatch(fd, tunnels, syscall.IN_CREATE); err != nil {
		t.Fatalf("inotify_add_watch: %v", err)
	}

	wizard := countryRawWizard
	if err := s.ReplaceConfig(context.Background(), "awg10", sampleConf, "",
		ReplaceOptions{AmneziaCountry: &wizard}); err != nil {
		t.Fatalf("ReplaceConfig: %v", err)
	}

	writes := 0
	for _, name := range createdIn(t, fd) {
		if strings.HasPrefix(name, "awg10.json.tmp.") {
			writes++
		}
	}
	if writes != 1 {
		t.Fatalf("записей файла туннеля = %d, want 1: страна обязана ехать тем же мутатором, что и конфигурация", writes)
	}
	// Без этой проверки тест зелёный и когда страну не пишут вовсе.
	got, err := s.store.Get("awg10")
	if err != nil {
		t.Fatalf("Get: %v", err)
	}
	if got.AmneziaCountry != "de" || got.Peer.Endpoint != "192.0.2.1:51820" {
		t.Fatalf("в единственной записи ожидались и страна, и конфигурация: %+v", got)
	}
}

// onlyStored возвращает единственную запись стора: ID импортированного
// туннеля выбирает сам сервис.
func onlyStored(t *testing.T, s *ServiceImpl) storage.AWGTunnel {
	t.Helper()
	list, err := s.store.List()
	if err != nil {
		t.Fatalf("List: %v", err)
	}
	if len(list) != 1 {
		t.Fatalf("записей в сторе = %d, want 1", len(list))
	}
	return list[0]
}

// createdIn вычитывает очередь inotify целиком и отдаёт имена файлов,
// созданных в каталоге. События кладутся в очередь внутри самой операции,
// поэтому к возврату ReplaceConfig они уже там — от времени тест не зависит.
func createdIn(t *testing.T, fd int) []string {
	t.Helper()
	const header = 16 // int32 wd + uint32 mask + uint32 cookie + uint32 len
	var out []string
	buf := make([]byte, 16*1024)
	for {
		n, err := syscall.Read(fd, buf)
		if err == syscall.EAGAIN {
			return out
		}
		if err != nil {
			t.Fatalf("чтение очереди inotify: %v", err)
		}
		for off := 0; off+header <= n; {
			nameLen := int(binary.NativeEndian.Uint32(buf[off+12:]))
			name := string(bytes.SplitN(buf[off+header:off+header+nameLen], []byte{0}, 2)[0])
			out = append(out, name)
			off += header + nameLen
		}
	}
}
