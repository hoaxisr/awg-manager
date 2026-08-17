package wdtt

import (
	"errors"
	"os"
	"path/filepath"
	"syscall"
	"testing"
	"time"
)

// Стражи признаков, которые ручки абонентов отдают наружу: частичный успех
// добавления, «пароль == главный», «заведён автоматически» и судьба SIGHUP.
// Каждый из них существует ровно потому, что вычислить его на фронте нечем.

func serverClientEntry(t *testing.T, st ServerClientsStatus, password string) ServerClientEntry {
	t.Helper()
	for _, u := range st.Users {
		if u.Password == password {
			return u
		}
	}
	t.Fatalf("абонент с паролем %q не найден: %+v", password, st.Users)
	return ServerClientEntry{}
}

// --- Частичный успех добавления (абонент в конфиге, файл не записан) ---

// TestAddServerClient_FileNotWrittenIsPartialSuccess: отказ записи
// passwords.json — НЕ «абонента нет». Запись в wdtt.json уже сделана и откату не
// подлежит (порядок «конфиг → файл» держит инвариант непустоты), поэтому ручка
// обязана отдавать отдельный исход: абонент есть, доступ появится при следующем
// запуске сервера.
//
// Отказ записи делается правами каталога — тот же приём, что в
// TestServerClients_NoReloadWhenFileWriteFails.
func TestAddServerClient_FileNotWrittenIsPartialSuccess(t *testing.T) {
	if os.Geteuid() == 0 {
		t.Skip("root игнорирует права каталога — отказ записи не воспроизвести")
	}
	s, cfgDir := newServerClientsService(t, "mainpass0000000000000000")
	if _, err := os.Stat(passwordsJSONPath(cfgDir)); !os.IsNotExist(err) {
		t.Fatalf("предпосылка теста нарушена: passwords.json уже существует (%v)", err)
	}
	if err := os.Chmod(cfgDir, 0500); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.Chmod(cfgDir, 0700) })

	_, err := s.AddServerClient(DefaultInstanceID, "abonent1", "Иван", "", "")
	if err == nil {
		t.Fatal("запись в закрытый каталог обязана вернуть ошибку")
	}
	if !errors.Is(err, ErrServerClientFileNotWritten) {
		t.Fatalf("частичный успех неотличим от полного отказа: %v", err)
	}
	// Причина обязана доехать до пользователя вместе с признаком: «read-only
	// file system» и «нет места» лечатся по-разному.
	if !errors.Is(err, os.ErrPermission) {
		t.Fatalf("причина отказа потеряна при заворачивании: %v", err)
	}
	if !configHasServerClient(t, s, "abonent1") {
		t.Fatal("признак частичного успеха соврал: абонента нет и в wdtt.json")
	}
}

// TestAddServerClient_PlainRefusalIsNotPartialSuccess — граница признака: отказ
// ДО записи конфига (занятый пароль) частичным успехом не является, иначе UI
// объявит созданным абонента, которого нет.
func TestAddServerClient_PlainRefusalIsNotPartialSuccess(t *testing.T) {
	s, _ := newServerClientsService(t, "mainpass0000000000000000")
	if _, err := s.AddServerClient(DefaultInstanceID, "abonent1", "Иван", "", ""); err != nil {
		t.Fatal(err)
	}
	_, err := s.AddServerClient(DefaultInstanceID, "abonent1", "Пётр", "", "")
	if err == nil {
		t.Fatal("повторный пароль обязан быть отвергнут")
	}
	if errors.Is(err, ErrServerClientFileNotWritten) {
		t.Fatalf("обычный отказ выдан за частичный успех: %v", err)
	}
	if errors.Is(err, ErrServerMainPasswordNotSaved) {
		t.Fatalf("обычный отказ выдан за несохранённый пароль сервера: %v", err)
	}
}

// TestAddServerClient_MainPasswordNotSavedIsPartialSuccess — второй частичный
// успех той же ручки: у первого абонента главный пароль приходит формой, и
// сохраняется он ПОСЛЕ абонента. Отказ этого сохранения абонента не отменяет —
// он в wdtt.json, в passwords.json и уже принят живым сервером, — но сервер без
// сохранённого пароля не стартует («укажите пароль подключения»), поэтому исход
// обязан отличаться и от полного отказа, и от «файл не записан».
//
// Отказ хранилища наводится правами каталога данных в момент, когда абонент уже
// применён: шов signalProc зовётся ровно между записью passwords.json и
// сохранением конфига, другой точки внутри операции нет.
func TestAddServerClient_MainPasswordNotSavedIsPartialSuccess(t *testing.T) {
	if os.Geteuid() == 0 {
		t.Skip("root игнорирует права каталога — отказ записи не воспроизвести")
	}
	const main = "mainpass0000000000000000"
	s, _ := newServerClientsService(t, "")
	dataDir := s.dataDir
	// Возврат прав — раньше уборки TempDir (t.Cleanup идёт в обратном порядке):
	// иначе RemoveAll не разберёт каталог и уронит прогон.
	t.Cleanup(func() { _ = os.Chmod(dataDir, 0700) })
	mark := filepath.Join(t.TempDir(), "hup.mark")
	startMarkedServerProcess(t, s, DefaultInstanceID, mark)

	proc := s.serverProcs.get(DefaultInstanceID)
	proc.signalProc = func(int, syscall.Signal) error {
		if err := os.Chmod(dataDir, 0500); err != nil {
			t.Errorf("chmod: %v", err)
		}
		// Сам сигнал этому тесту не нужен: он про судьбу пароля сервера.
		return nil
	}

	_, err := s.AddServerClient(DefaultInstanceID, "abonent1", "Иван", "", main)
	if err == nil {
		t.Fatal("отказ сохранения конфига обязан вернуть ошибку")
	}
	if !errors.Is(err, ErrServerMainPasswordNotSaved) {
		t.Fatalf("частичный успех неотличим от полного отказа: %v", err)
	}
	if errors.Is(err, ErrServerClientFileNotWritten) {
		t.Fatalf("несохранённый пароль сервера выдан за незаписанный файл: %v", err)
	}
	// Причина обязана доехать вместе с признаком: «нет прав» и «нет места»
	// лечатся по-разному.
	if !errors.Is(err, os.ErrPermission) {
		t.Fatalf("причина отказа потеряна при заворачивании: %v", err)
	}
	if !configHasServerClient(t, s, "abonent1") {
		t.Fatal("признак частичного успеха соврал: абонента нет в wdtt.json")
	}
	// Названная потеря: пароль сервера действительно не сохранён.
	if pass := serverConfigOf(t, s, DefaultInstanceID).Password; pass != "" {
		t.Fatalf("предпосылка теста нарушена: пароль сервера сохранился (%q)", pass)
	}
}

// --- Признак «пароль абонента совпадает с главным» ---

// TestListServerClients_MarksMainPasswordEntry: сам главный пароль наружу не
// уходит (это ключ администрирования), поэтому сравнивать на фронте нечем —
// список несёт готовый признак. Запись с таким паролем в конфиг попадает из
// старых конфигов и ручной правки; ручка добавления её заводить не даёт.
func TestListServerClients_MarksMainPasswordEntry(t *testing.T) {
	const main = "mainpass0000000000000000"
	s, _ := newServerClientsService(t, main)
	setServerClients(t, s, []ServerClient{
		{Password: main, Comment: "Наследие"},
		{Password: "abonent1", Comment: "Иван"},
	})

	st, err := s.ListServerClients(DefaultInstanceID)
	if err != nil {
		t.Fatal(err)
	}
	if !serverClientEntry(t, st, main).IsMainPassword {
		t.Fatalf("абонент с главным паролем не помечен: %+v", st.Users)
	}
	if serverClientEntry(t, st, "abonent1").IsMainPassword {
		t.Fatalf("обычный абонент помечен главным паролем: %+v", st.Users)
	}
	// Признак не подменяет собой срок: сервер такого абонента не примет, но
	// «истёк» — про другое, и путать их в UI нельзя.
	if serverClientEntry(t, st, main).IsExpired {
		t.Fatalf("абонент с главным паролем помечен просроченным: %+v", st.Users)
	}
}

// TestMergeServerClients_MainPasswordComparedTrimmed — пароль в конфиге и
// главный пароль сравниваются подрезанными, как во всём конвейере: иначе
// унаследованный " main " не получил бы признака и выглядел бы рабочим.
func TestMergeServerClients_MainPasswordComparedTrimmed(t *testing.T) {
	st := mergeServerClients(
		[]ServerClient{{Password: " main "}},
		nil,
		false,
		"main",
		time.Unix(1700000000, 0),
	)
	if !st.Users[0].IsMainPassword {
		t.Fatalf("пароль абонента с пробелами не признан главным: %+v", st.Users[0])
	}
	// Вторая сторона того же сравнения: пробелы в САМОМ главном пароле.
	// Хранилище тримит его на входе (normalizeServerConfig), но конфиг
	// переживает старые версии и ручную правку.
	paddedMain := mergeServerClients(
		[]ServerClient{{Password: "main"}},
		nil,
		false,
		" main ",
		time.Unix(1700000000, 0),
	)
	if !paddedMain.Users[0].IsMainPassword {
		t.Fatalf("главный пароль с пробелами не признан совпадением: %+v", paddedMain.Users[0])
	}
	// Пустой главный пароль совпадением не считается: сервер без пароля не
	// запускается, и бейдж у каждого абонента был бы ложью.
	empty := mergeServerClients(
		[]ServerClient{{Password: "abonent1"}},
		nil,
		false,
		"   ",
		time.Unix(1700000000, 0),
	)
	if empty.Users[0].IsMainPassword {
		t.Fatalf("пустой главный пароль совпал с абонентом: %+v", empty.Users[0])
	}
}

// --- Признак «заведён автоматически» ---

// TestServerClients_AutoFlagOnFirstSupport: опора 1 (сохранение пароля сервера)
// заводит «Абонента 1» — он обязан быть помечен в хранилище и в списке.
func TestServerClients_AutoFlagOnFirstSupport(t *testing.T) {
	s, _ := newServerClientsService(t, "mainpass0000000000000000")
	clients := configServerClients(t, s)
	if len(clients) != 1 {
		t.Fatalf("опора 1 не завела абонента: %+v", clients)
	}
	if !clients[0].Auto {
		t.Fatalf("автоматический абонент не помечен в wdtt.json: %+v", clients[0])
	}
	st, err := s.ListServerClients(DefaultInstanceID)
	if err != nil {
		t.Fatal(err)
	}
	if !serverClientEntry(t, st, clients[0].Password).IsAuto {
		t.Fatalf("признак авто-создания не доехал до списка: %+v", st.Users)
	}
}

// TestServerClients_AutoFlagOnFileSupport: опора 2 (ensureUsableServerClient на
// пути записи файла) — второй источник автоматических абонентов, и он обязан
// метить их так же.
func TestServerClients_AutoFlagOnFileSupport(t *testing.T) {
	const main = "mainpass0000000000000000"
	s, cfgDir := newServerClientsService(t, main)
	expired := time.Now().Add(-time.Hour).Unix()
	setServerClients(t, s, []ServerClient{{Password: "abonent1", Comment: "Иван", ExpiresAt: expired}})

	if err := s.syncServerClientsOnStart(DefaultInstanceID, cfgDir, serverConfigOf(t, s, DefaultInstanceID)); err != nil {
		t.Fatalf("syncServerClientsOnStart: %v", err)
	}
	var auto *ServerClient
	for i, c := range configServerClients(t, s) {
		if c.Comment == defaultServerClientName {
			auto = &configServerClients(t, s)[i]
			break
		}
	}
	if auto == nil {
		t.Fatalf("опора 2 не завела абонента: %+v", configServerClients(t, s))
	}
	if !auto.Auto {
		t.Fatalf("автоматический абонент опоры 2 не помечен: %+v", *auto)
	}
	st, err := s.ListServerClients(DefaultInstanceID)
	if err != nil {
		t.Fatal(err)
	}
	if !serverClientEntry(t, st, auto.Password).IsAuto {
		t.Fatalf("признак авто-создания опоры 2 не доехал до списка: %+v", st.Users)
	}
	if serverClientEntry(t, st, "abonent1").IsAuto {
		t.Fatalf("заведённый человеком абонент помечен автоматическим: %+v", st.Users)
	}
}

// TestAddServerClient_IsNotAuto — абонент, заказанный человеком, признака не
// получает: иначе бейдж «заведён автоматически» повиснет на всех.
func TestAddServerClient_IsNotAuto(t *testing.T) {
	s, _ := newServerClientsService(t, "mainpass0000000000000000")
	st, err := s.AddServerClient(DefaultInstanceID, "abonent1", "Иван", "", "")
	if err != nil {
		t.Fatal(err)
	}
	if serverClientEntry(t, st, "abonent1").IsAuto {
		t.Fatalf("абонент человека помечен автоматическим: %+v", st.Users)
	}
}

// TestServerClients_AutoFlagSurvivesRenameAndAdoption — признак хранится, а
// значит его можно потерять правкой записи. Два пути правки: переименование
// (setServerClientComment) и мерж срока из passwords.json (усыновление).
// Матч по имени «Абонент 1» именно поэтому и не годится: после переименования
// он врёт, а признак обязан пережить обе операции.
func TestServerClients_AutoFlagSurvivesRenameAndAdoption(t *testing.T) {
	const main = "mainpass0000000000000000"
	s, cfgDir := newServerClientsService(t, main)
	auto := configServerClients(t, s)[0]

	if _, err := s.RenameServerClient(DefaultInstanceID, auto.Password, "Мой ноутбук"); err != nil {
		t.Fatalf("RenameServerClient: %v", err)
	}
	renamed := configServerClients(t, s)[0]
	if renamed.Comment != "Мой ноутбук" {
		t.Fatalf("предпосылка теста нарушена, имя не сменилось: %+v", renamed)
	}
	if !renamed.Auto {
		t.Fatalf("переименование сняло признак авто-создания: %+v", renamed)
	}

	// Усыновление: сервер выдал этой же записи срок через admin-API, и мерж
	// обязан внести только срок.
	expires := time.Now().Add(24 * time.Hour).Unix()
	writePasswordsFixture(t, cfgDir, passwordsJSON{
		MainPassword: main,
		Passwords:    map[string]passwordsJSONUser{auto.Password: {Label: "Из бота", ExpiresAt: expires}},
	})
	st, err := s.ListServerClients(DefaultInstanceID)
	if err != nil {
		t.Fatal(err)
	}
	adopted := configServerClients(t, s)[0]
	if adopted.ExpiresAt != expires {
		t.Fatalf("предпосылка теста нарушена, срок не усыновлён: %+v", adopted)
	}
	if !adopted.Auto {
		t.Fatalf("усыновление срока сняло признак авто-создания: %+v", adopted)
	}
	if !serverClientEntry(t, st, auto.Password).IsAuto {
		t.Fatalf("признак авто-создания потерян в списке после усыновления: %+v", st.Users)
	}
}

// --- Судьба SIGHUP (применено сейчас / применится при запуске) ---

// TestServerClients_ReloadReportsStoppedServer: сервер не запущен — файл
// записан, состав вступит в силу при следующем запуске. Ручка обязана сказать
// это прямо, а не отвечать голым успехом.
func TestServerClients_ReloadReportsStoppedServer(t *testing.T) {
	s, _ := newServerClientsService(t, "mainpass0000000000000000")
	st, err := s.AddServerClient(DefaultInstanceID, "abonent1", "Иван", "", "")
	if err != nil {
		t.Fatal(err)
	}
	if st.Reload != ReloadServerStopped {
		t.Fatalf("добавление на остановленном сервере: reload = %q", st.Reload)
	}
	rm, err := s.RemoveServerClient(DefaultInstanceID, "abonent1")
	if err != nil {
		t.Fatal(err)
	}
	if rm.Reload != ReloadServerStopped {
		t.Fatalf("удаление на остановленном сервере: reload = %q", rm.Reload)
	}
}

// TestServerClients_ReloadReportsDelivery: живой сервер получил SIGHUP —
// только в этом случае «применено сейчас» правда.
func TestServerClients_ReloadReportsDelivery(t *testing.T) {
	s, _ := newServerClientsService(t, "mainpass0000000000000000")
	mark := filepath.Join(t.TempDir(), "hup.mark")
	startMarkedServerProcess(t, s, DefaultInstanceID, mark)

	st, err := s.AddServerClient(DefaultInstanceID, "abonent1", "Иван", "", "")
	if err != nil {
		t.Fatal(err)
	}
	// Сигнал действительно принят: без этого признак — необоснованное обещание.
	waitForHupCount(t, mark, 1, 3*time.Second)
	if st.Reload != ReloadDelivered {
		t.Fatalf("живой сервер получил SIGHUP, а ручка отдала reload = %q", st.Reload)
	}

	rm, err := s.RemoveServerClient(DefaultInstanceID, "abonent1")
	if err != nil {
		t.Fatal(err)
	}
	waitForHupCount(t, mark, 2, 3*time.Second)
	if rm.Reload != ReloadDelivered {
		t.Fatalf("удаление у живого сервера: reload = %q", rm.Reload)
	}
}

// TestServerClients_ReloadReportsFailedDelivery: процесс живой, а сигнал не
// ушёл. Файл записан, значит доступ появится при следующем запуске, но
// «применено сейчас» обещать нельзя — исход обязан отличаться и от доставки, и
// от остановленного сервера.
//
// Отказ доставки воспроизводится швом signalProc: kill своему живому ребёнку не
// отказывает, другого способа получить эту ветку нет.
func TestServerClients_ReloadReportsFailedDelivery(t *testing.T) {
	s, _ := newServerClientsService(t, "mainpass0000000000000000")
	mark := filepath.Join(t.TempDir(), "hup.mark")
	startMarkedServerProcess(t, s, DefaultInstanceID, mark)

	proc := s.serverProcs.get(DefaultInstanceID)
	if running, _ := proc.IsRunning(); !running {
		t.Fatal("предпосылка теста нарушена: процесс сервера не запущен")
	}
	proc.signalProc = func(int, syscall.Signal) error { return errors.New("operation not permitted") }

	st, err := s.AddServerClient(DefaultInstanceID, "abonent1", "Иван", "", "")
	if err != nil {
		t.Fatal(err)
	}
	if st.Reload != ReloadFailed {
		t.Fatalf("сигнал не доставлен живому серверу, а ручка отдала reload = %q", st.Reload)
	}
	// Отказ доставки не отменяет операции: абонент записан и подхватится
	// следующим запуском.
	if !configHasServerClient(t, s, "abonent1") {
		t.Fatal("абонент потерян из-за недоставленного сигнала")
	}
}

// TestServerClients_ReloadDeadProcessIsStoppedServer — процесс умер между
// проверкой живости и отправкой сигнала: ядро отвечает ESRCH. Для пользователя
// это тот же исход, что остановленный сервер (файл записан, состав вступит при
// следующем запуске), а не «не удалось применить» — отказ доставки
// подразумевает живого адресата, которому мы не достучались.
//
// Гонка воспроизводится швом signalProc: реальное окно между IsRunning и kill
// микроскопично и наведению не поддаётся.
func TestServerClients_ReloadDeadProcessIsStoppedServer(t *testing.T) {
	s, _ := newServerClientsService(t, "mainpass0000000000000000")
	mark := filepath.Join(t.TempDir(), "hup.mark")
	startMarkedServerProcess(t, s, DefaultInstanceID, mark)

	proc := s.serverProcs.get(DefaultInstanceID)
	if running, _ := proc.IsRunning(); !running {
		t.Fatal("предпосылка теста нарушена: процесс сервера не запущен")
	}
	proc.signalProc = func(int, syscall.Signal) error { return syscall.ESRCH }

	st, err := s.AddServerClient(DefaultInstanceID, "abonent1", "Иван", "", "")
	if err != nil {
		t.Fatal(err)
	}
	if st.Reload != ReloadServerStopped {
		t.Fatalf("процесс умер до сигнала, а ручка отдала reload = %q", st.Reload)
	}
	if !configHasServerClient(t, s, "abonent1") {
		t.Fatal("абонент потерян из-за умершего процесса")
	}

	// Граница: ESRCH — единственная ошибка со смыслом «адресата уже нет».
	// Любая другая остаётся отказом доставки, иначе признак перестанет ловить
	// живой сервер, до которого не достучались.
	proc.signalProc = func(int, syscall.Signal) error { return syscall.EPERM }
	rm, err := s.RemoveServerClient(DefaultInstanceID, "abonent1")
	if err != nil {
		t.Fatal(err)
	}
	if rm.Reload != ReloadFailed {
		t.Fatalf("отказ доставки живому серверу выдан за остановленный: reload = %q", rm.Reload)
	}
}

// TestRenameServerClient_ReloadIsEmpty — переименование passwords.json не
// переписывает и сервер не пинает; отдавать «применено сейчас» ему нечего.
func TestRenameServerClient_ReloadIsEmpty(t *testing.T) {
	s, _ := newServerClientsService(t, "mainpass0000000000000000")
	if _, err := s.AddServerClient(DefaultInstanceID, "abonent1", "Иван", "", ""); err != nil {
		t.Fatal(err)
	}
	st, err := s.RenameServerClient(DefaultInstanceID, "abonent1", "Пётр")
	if err != nil {
		t.Fatal(err)
	}
	if st.Reload != "" {
		t.Fatalf("переименование доложило о доставке сигнала: reload = %q", st.Reload)
	}
}

// TestListServerClients_ReloadIsEmpty — чтение списка ничего не применяет.
func TestListServerClients_ReloadIsEmpty(t *testing.T) {
	s, _ := newServerClientsService(t, "mainpass0000000000000000")
	st, err := s.ListServerClients(DefaultInstanceID)
	if err != nil {
		t.Fatal(err)
	}
	if st.Reload != "" {
		t.Fatalf("чтение списка доложило о судьбе сигнала: reload = %q", st.Reload)
	}
}
