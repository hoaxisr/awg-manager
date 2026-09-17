package ftlink

import (
	"encoding/json"
	"fmt"
	"os"
	"strings"
	"sync"

	"github.com/hoaxisr/awg-manager/internal/storage"
)

// linksFile — выданные ссылки абонентов одного freeturn-сервера: Client ID →
// freeturn://…  (#919). Своя структура, а не поле в файле списка: список
// (`clients.json`) читает форк-сервер, и класть в его файл приватный ключ пира
// из ссылки незачем.
//
// Ключ — тот же приведённый к нижнему регистру Client ID, каким его кладёт в
// список addAllowlistClient: иначе ссылка не нашлась бы по записи списка.
type linksFile struct {
	Links map[string]string `json:"links"`
}

func readLinksFile(path string) (linksFile, error) {
	var data linksFile
	b, err := os.ReadFile(path)
	if err != nil {
		if os.IsNotExist(err) {
			data.Links = map[string]string{}
			return data, nil
		}
		return data, err
	}
	if err := json.Unmarshal(b, &data); err != nil {
		return data, fmt.Errorf("разбор файла ссылок %s: %w", path, err)
	}
	if data.Links == nil {
		data.Links = map[string]string{}
	}
	return data, nil
}

// linksMu сериализует чтение-правку-запись файла ссылок. Писатель один —
// сам демон, — но ручек, доходящих сюда, две (выдача ссылки и удаление
// абонента), и параллельные запросы панели без замка теряли бы правку.
var linksMu sync.Mutex

// writeLinksFile — запись общим помощником: уникальный временный файл, права
// 0600 (в ссылке едет приватный ключ пира) и fsync файла и каталога перед
// переименованием. Своя копия этого приёма была бы четвёртой в репозитории и
// без sync — на роутере пропадание питания штатно (F374).
func writeLinksFile(path string, data linksFile) error {
	if data.Links == nil {
		data.Links = map[string]string{}
	}
	b, err := json.MarshalIndent(data, "", "  ")
	if err != nil {
		return err
	}
	return storage.AtomicWritePerm(path, b, storage.SecretFilePermission)
}

// setClientLink запоминает ссылку абонента. Перевыпуск затирает прежнюю: две
// ссылки на один Client ID — это одна рабочая и одна мёртвая, и показать
// панель обязана ПОСЛЕДНЮЮ.
//
// Известный потолок: ссылка запоминается и тогда, когда владелец снял галку
// «внести в список» — записи списка у неё не будет, и в UI она не всплывёт.
// Такой осиротевший ключ вычистит только удаление инстанса.
func setClientLink(path, clientID, link string) error {
	id := strings.ToLower(strings.TrimSpace(clientID))
	if id == "" || strings.TrimSpace(link) == "" {
		return nil
	}
	linksMu.Lock()
	defer linksMu.Unlock()
	data, err := readLinksFile(path)
	if err != nil {
		return err
	}
	data.Links[id] = link
	return writeLinksFile(path, data)
}

// dropClientLink снимает ссылку вычеркнутого абонента: без записи списка
// показать её всё равно негде, а приватный ключ пира лежал бы дальше.
func dropClientLink(path, clientID string) error {
	id := strings.ToLower(strings.TrimSpace(clientID))
	if id == "" {
		return nil
	}
	linksMu.Lock()
	defer linksMu.Unlock()
	data, err := readLinksFile(path)
	if err != nil {
		return err
	}
	if _, ok := data.Links[id]; !ok {
		return nil
	}
	delete(data.Links, id)
	return writeLinksFile(path, data)
}

// clientLinks — все ссылки сервера. Отсутствующий файл — не ошибка: у
// абонентов, заведённых до появления файла, ссылки нет.
func clientLinks(path string) (map[string]string, error) {
	data, err := readLinksFile(path)
	if err != nil {
		return nil, err
	}
	return data.Links, nil
}
