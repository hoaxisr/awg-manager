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

// Ссылка, выданная абоненту, переживает перезагрузку страницы: её показывает
// список — это и есть #919. Проверяется сквозь два шва (сборщик пишет, сервис
// читает): именно их расхождение и ломает фичу.
func TestBuildLink_IssuedLinkComesBackInList(t *testing.T) {
	s, _, _, dataDir := newAllowlistService(t, ftServerRecord(""))
	b := NewBuilder(BuilderDeps{ExternalIP: (&fakeExternalIP{ip: "1.2.3.4"}).get, DataDir: dataDir})

	body, _ := buildLink(t, b, ftServerRecord(""), wdttlink.LinkRequest{ClientID: upperClientID})
	if _, err := s.Add(context.Background(), ftServerKey, upperClientID, "Телефон Ивана"); err != nil {
		t.Fatal(err)
	}

	st, err := s.List(ftServerKey)
	if err != nil {
		t.Fatal(err)
	}
	if len(st.Clients) != 1 {
		t.Fatalf("записей=%d, want 1: %+v", len(st.Clients), st.Clients)
	}
	if st.Clients[0].Link != body["link"] {
		t.Fatalf("ссылка записи=%q, want %q", st.Clients[0].Link, body["link"])
	}
}

// Ответ на добавление — тот же состав, что у List: по нему фронт перерисовывает
// список сразу, не перезагружая страницу. Без ссылок в ответе кнопка «Ссылка»
// у только что внесённого абонента появлялась бы лишь после перезагрузки.
func TestAllowlist_AddAnswersWithLink(t *testing.T) {
	s, _, _, dataDir := newAllowlistService(t, ftServerRecord(""))
	b := NewBuilder(BuilderDeps{ExternalIP: (&fakeExternalIP{ip: "1.2.3.4"}).get, DataDir: dataDir})
	body, _ := buildLink(t, b, ftServerRecord(""), wdttlink.LinkRequest{ClientID: upperClientID})

	res, err := s.Add(context.Background(), ftServerKey, upperClientID, "Телефон Ивана")
	if err != nil {
		t.Fatal(err)
	}
	if len(res.Clients) != 1 || res.Clients[0].Link != body["link"] {
		t.Fatalf("ответ Add=%+v, ждали ссылку %q", res.Clients, body["link"])
	}
}

// Перевыпуск затирает прежнюю ссылку: показывать надо ПОСЛЕДНЮЮ, старая мертва.
func TestBuildLink_ReissueReplacesStoredLink(t *testing.T) {
	s, _, _, dataDir := newAllowlistService(t, ftServerRecord(""))
	rec := ftServerRecord("")
	b := NewBuilder(BuilderDeps{ExternalIP: (&fakeExternalIP{ip: "1.2.3.4"}).get, DataDir: dataDir})

	first, _ := buildLink(t, b, rec, wdttlink.LinkRequest{ClientID: upperClientID})
	if _, err := s.Add(context.Background(), ftServerKey, upperClientID, "Телефон Ивана"); err != nil {
		t.Fatal(err)
	}
	// Другой peer — другая ссылка: так видно, что вернулась именно вторая.
	second, _ := buildLink(t, b, rec, wdttlink.LinkRequest{ClientID: upperClientID, Peer: "9.9.9.9:56000"})
	if first["link"] == second["link"] {
		t.Fatal("подготовка теста: ссылки совпали, подмену не отличить")
	}

	st, err := s.List(ftServerKey)
	if err != nil {
		t.Fatal(err)
	}
	if st.Clients[0].Link != second["link"] {
		t.Fatalf("ссылка записи=%q, want вторую %q", st.Clients[0].Link, second["link"])
	}
}

// Ссылка без Client ID никому не принадлежит: запоминать её не за что.
func TestBuildLink_WithoutClientIDStoresNothing(t *testing.T) {
	dataDir := t.TempDir()
	b := NewBuilder(BuilderDeps{ExternalIP: (&fakeExternalIP{ip: "1.2.3.4"}).get, DataDir: dataDir})

	buildLink(t, b, ftServerRecord(""), wdttlink.LinkRequest{})

	path := instancestore.FreeTurnLinksPath(dataDir, "default")
	if _, err := os.Stat(path); !os.IsNotExist(err) {
		t.Fatalf("файл ссылок %s создан на ссылке без Client ID (err=%v)", path, err)
	}
}

// Вычеркнутый абонент уносит свою ссылку: показать её негде, а приватный ключ
// пира лежал бы в файле дальше.
func TestAllowlist_RemoveDropsStoredLink(t *testing.T) {
	s, _, _, dataDir := newAllowlistService(t, ftServerRecord(""))
	b := NewBuilder(BuilderDeps{ExternalIP: (&fakeExternalIP{ip: "1.2.3.4"}).get, DataDir: dataDir})
	buildLink(t, b, ftServerRecord(""), wdttlink.LinkRequest{ClientID: upperClientID})
	if _, err := s.Add(context.Background(), ftServerKey, upperClientID, "Телефон Ивана"); err != nil {
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

// В файле ссылок лежит приватный ключ пира: чужим он не читается. Проверяется
// и временный файл — его os.WriteFile существующему файлу правами не наделяет,
// поэтому запись идёт через os.CreateTemp с явным Chmod.
func TestLinksFile_NotWorldReadable(t *testing.T) {
	dataDir := t.TempDir()
	path := instancestore.FreeTurnLinksPath(dataDir, "default")
	if err := setClientLink(path, upperClientID, "freeturn://x"); err != nil {
		t.Fatal(err)
	}
	// Вторая запись идёт поверх существующего файла — права не должны «уплыть».
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
	left, err := filepath.Glob(filepath.Join(filepath.Dir(path), "*.tmp"))
	if err != nil {
		t.Fatal(err)
	}
	if len(left) != 0 {
		t.Fatalf("после записи остались временные файлы: %v", left)
	}
}

// Без каталога данных писать некуда: относительный путь увёл бы файл с ключом
// в рабочий каталог демона.
func TestBuildLink_WithoutDataDirWritesNothing(t *testing.T) {
	t.Chdir(t.TempDir())
	b := NewBuilder(BuilderDeps{ExternalIP: (&fakeExternalIP{ip: "1.2.3.4"}).get})

	buildLink(t, b, ftServerRecord(""), wdttlink.LinkRequest{ClientID: upperClientID})

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
	if _, err := s.Add(context.Background(), ftServerKey, okClientID, "Старый"); err != nil {
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
