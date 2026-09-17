package ftlink

import (
	"context"
	"os"
	"path/filepath"
	"testing"

	"github.com/hoaxisr/awg-manager/internal/proxyapp/wdttlink"
	"github.com/hoaxisr/awg-manager/internal/proxyrt/instancestore"
)

// Client ID в ВЕРХНЕМ регистре: список кладёт ключ приведённым к нижнему
// (addAllowlistClient), и ссылка обязана находиться по той же записи. Ключи,
// разъехавшиеся по регистру, дали бы «абонент есть, ссылки нет».
const upperClientID = "AABBCCDDEEFF00112233445566778899"

const someLink = "freeturn://eyJ2IjoxfQ"

// Ссылка, выданная абоненту, переживает перезагрузку страницы: её показывает
// список — это и есть #919.
func TestAllowlist_AddStoresLinkAndListReturnsIt(t *testing.T) {
	s, _, _, _ := newAllowlistService(t, ftServerRecord(""))

	if _, err := s.Add(context.Background(), ftServerKey, upperClientID, "Телефон Ивана", someLink); err != nil {
		t.Fatal(err)
	}

	st, err := s.List(ftServerKey)
	if err != nil {
		t.Fatal(err)
	}
	if len(st.Clients) != 1 {
		t.Fatalf("записей=%d, want 1: %+v", len(st.Clients), st.Clients)
	}
	if st.Clients[0].Link != someLink {
		t.Fatalf("ссылка записи=%q, want %q", st.Clients[0].Link, someLink)
	}
}

// Ответ на добавление — тот же состав, что у List: по нему фронт перерисовывает
// список сразу, не перезагружая страницу. Без ссылок в ответе кнопка «Ссылка»
// у только что внесённого абонента появлялась бы лишь после перезагрузки.
func TestAllowlist_AddAnswersWithLink(t *testing.T) {
	s, _, _, _ := newAllowlistService(t, ftServerRecord(""))

	res, err := s.Add(context.Background(), ftServerKey, upperClientID, "Телефон Ивана", someLink)
	if err != nil {
		t.Fatal(err)
	}
	if len(res.Clients) != 1 || res.Clients[0].Link != someLink {
		t.Fatalf("ответ Add=%+v, ждали ссылку %q", res.Clients, someLink)
	}
}

// Перевыпуск затирает прежнюю ссылку: показывать надо ПОСЛЕДНЮЮ, старая мертва.
func TestAllowlist_AddReplacesStoredLink(t *testing.T) {
	s, _, _, _ := newAllowlistService(t, ftServerRecord(""))
	ctx := context.Background()
	if _, err := s.Add(ctx, ftServerKey, upperClientID, "Телефон Ивана", someLink); err != nil {
		t.Fatal(err)
	}

	res, err := s.Add(ctx, ftServerKey, upperClientID, "Телефон Ивана", "freeturn://second")
	if err != nil {
		t.Fatal(err)
	}
	if res.Clients[0].Link != "freeturn://second" {
		t.Fatalf("ссылка записи=%q, want вторую", res.Clients[0].Link)
	}
}

// Переименование абонента идёт тем же Add, но ссылки в теле не несёт — и
// стирать сохранённую не должно: показать её после переименования по-прежнему
// надо.
func TestAllowlist_AddWithoutLinkKeepsStored(t *testing.T) {
	s, _, _, _ := newAllowlistService(t, ftServerRecord(""))
	ctx := context.Background()
	if _, err := s.Add(ctx, ftServerKey, upperClientID, "Телефон Ивана", someLink); err != nil {
		t.Fatal(err)
	}

	res, err := s.Add(ctx, ftServerKey, upperClientID, "Телефон Ивана (2)", "")
	if err != nil {
		t.Fatal(err)
	}
	if res.Clients[0].Link != someLink {
		t.Fatalf("ссылка записи=%q, want прежнюю %q", res.Clients[0].Link, someLink)
	}
}

// Отказ записи списка уносит и ссылку: записи не будет, показать ссылку негде,
// а в файле остался бы приватный ключ пира (F370).
func TestAllowlist_AddDropsLinkWhenListRefuses(t *testing.T) {
	s, _, _, dataDir := newAllowlistService(t, ftServerRecord(""))

	// Невалидный Client ID отвергает addAllowlistClient — уже ПОСЛЕ записи ссылки.
	if _, err := s.Add(context.Background(), ftServerKey, "не-hex", "Телефон Ивана", someLink); err == nil {
		t.Fatal("ждали отказ на невалидном Client ID")
	}

	links, err := clientLinks(instancestore.FreeTurnLinksPath(dataDir, "default"))
	if err != nil {
		t.Fatal(err)
	}
	if len(links) != 0 {
		t.Fatalf("после отказа в файле ссылок осталось: %+v", links)
	}
}

// Ручка выдачи ссылку НЕ сохраняет: абонента в список могли и не внести, и
// тогда ссылка стала бы сиротой (F370). Сторож против возврата записи туда.
func TestBuildLink_StoresNothing(t *testing.T) {
	dataDir := t.TempDir()
	t.Chdir(t.TempDir())
	b := NewBuilder(BuilderDeps{ExternalIP: (&fakeExternalIP{ip: "1.2.3.4"}).get})

	buildLink(t, b, ftServerRecord(""), wdttlink.LinkRequest{ClientID: upperClientID})

	for _, dir := range []string{dataDir, "."} {
		left, err := filepath.Glob(filepath.Join(dir, "freeturn", "*"))
		if err != nil {
			t.Fatal(err)
		}
		if len(left) != 0 {
			t.Fatalf("выдача ссылки записала %v", left)
		}
	}
}

// Вычеркнутый абонент уносит свою ссылку: показать её негде, а приватный ключ
// пира лежал бы в файле дальше.
func TestAllowlist_RemoveDropsStoredLink(t *testing.T) {
	s, _, _, dataDir := newAllowlistService(t, ftServerRecord(""))
	if _, err := s.Add(context.Background(), ftServerKey, upperClientID, "Телефон Ивана", someLink); err != nil {
		t.Fatal(err)
	}

	if err := s.Remove(ftServerKey, upperClientID); err != nil {
		t.Fatal(err)
	}

	links, err := clientLinks(instancestore.FreeTurnLinksPath(dataDir, "default"))
	if err != nil {
		t.Fatal(err)
	}
	if len(links) != 0 {
		t.Fatalf("после Remove в файле ссылок осталось: %+v", links)
	}
}

// В файле ссылок лежит приватный ключ пира: чужим он не читается. Вторая запись
// идёт поверх существующего файла — права не должны «уплыть».
func TestLinksFile_NotWorldReadable(t *testing.T) {
	dataDir := t.TempDir()
	path := instancestore.FreeTurnLinksPath(dataDir, "default")
	if err := setClientLink(path, upperClientID, someLink); err != nil {
		t.Fatal(err)
	}
	if err := setClientLink(path, okClientID, "freeturn://y"); err != nil {
		t.Fatal(err)
	}

	st, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	if mode := st.Mode().Perm(); mode&0077 != 0 {
		t.Fatalf("права файла ссылок=%o, чужим он читаться не должен", mode)
	}
	// Временный файл с ключом не переживает запись.
	left, err := filepath.Glob(filepath.Join(filepath.Dir(path), "*.tmp*"))
	if err != nil {
		t.Fatal(err)
	}
	if len(left) != 0 {
		t.Fatalf("после записи остались временные файлы: %v", left)
	}
}

// Без каталога данных писать некуда: относительный путь увёл бы файл с ключом
// в рабочий каталог демона.
func TestAllowlist_AddWithoutDataDirWritesNothing(t *testing.T) {
	t.Chdir(t.TempDir())
	rec := ftServerRecord(filepath.Join(t.TempDir(), "clients.json"))
	src := &fakeSource{recs: map[string]instancestore.Record{rec.Key(): rec}}
	s := New(Deps{Records: src, Mutator: &fakeMutator{src: src}})

	if _, err := s.Add(context.Background(), ftServerKey, upperClientID, "Телефон Ивана", someLink); err != nil {
		t.Fatal(err)
	}

	left, err := filepath.Glob(filepath.Join("freeturn", "*"))
	if err != nil {
		t.Fatal(err)
	}
	if len(left) != 0 {
		t.Fatalf("в рабочем каталоге появилось: %v", left)
	}
}

// Абоненты, заведённые до появления файла ссылок, список не ломают.
func TestAllowlist_ListWithoutLinksFile(t *testing.T) {
	s, _, _, _ := newAllowlistService(t, ftServerRecord(""))
	if _, err := s.Add(context.Background(), ftServerKey, okClientID, "Старый", ""); err != nil {
		t.Fatal(err)
	}
	st, err := s.List(ftServerKey)
	if err != nil {
		t.Fatal(err)
	}
	if len(st.Clients) != 1 || st.Clients[0].Link != "" {
		t.Fatalf("записи=%+v", st.Clients)
	}
}
