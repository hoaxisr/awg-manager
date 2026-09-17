package ftlink

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
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

// writeLinksFile — атомарная запись 0600: в ссылке едет приватный ключ пира.
// Временный файл создаётся УНИКАЛЬНЫМ (os.CreateTemp), а не `path+".tmp"`:
// фиксированное имя два писателя переслоили бы, а чужой файл, оставшийся от
// прежнего отказа, ещё и переиспользовался бы с его правами — os.WriteFile
// существующему файлу прав не меняет.
func writeLinksFile(path string, data linksFile) error {
	if data.Links == nil {
		data.Links = map[string]string{}
	}
	dir := filepath.Dir(path)
	if err := os.MkdirAll(dir, 0755); err != nil {
		return err
	}
	b, err := json.MarshalIndent(data, "", "  ")
	if err != nil {
		return err
	}
	f, err := os.CreateTemp(dir, filepath.Base(path)+".*.tmp")
	if err != nil {
		return err
	}
	tmp := f.Name()
	// Дальше файл либо переедет на место, либо должен исчезнуть: временный
	// файл с приватным ключом пережить отказ не должен.
	defer os.Remove(tmp)
	if err := f.Chmod(0600); err != nil {
		f.Close()
		return err
	}
	if _, err := f.Write(b); err != nil {
		f.Close()
		return err
	}
	if err := f.Close(); err != nil {
		return err
	}
	return os.Rename(tmp, path)
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
