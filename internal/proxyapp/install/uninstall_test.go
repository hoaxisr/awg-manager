package install

import (
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"testing"
)

// countOf — гейт удаления: сколько инстансов у подсистемы.
func countOf(n map[Subsystem]int) func(Subsystem) (int, error) {
	return func(s Subsystem) (int, error) { return n[s], nil }
}

func TestUninstallRemovesBinariesAndVersionFile(t *testing.T) {
	s := newTestService(t, Deps{InstanceCount: countOf(nil)})
	sub := s.subs[SubsystemWdtt]
	writeBin(t, sub.clientBin, "client")
	writeBin(t, sub.serverBin, "server")
	if err := os.WriteFile(sub.versionPath, []byte(`{"version":"1"}`), 0o600); err != nil {
		t.Fatal(err)
	}

	if err := s.Uninstall("wdtt"); err != nil {
		t.Fatal(err)
	}
	for _, path := range []string{sub.clientBin, sub.serverBin, sub.versionPath} {
		if _, err := os.Stat(path); !os.IsNotExist(err) {
			t.Fatalf("%s пережил удаление: %v", path, err)
		}
	}
	// Соседняя подсистема не тронута: удаление идёт своей подсистемой.
	ft := s.subs[SubsystemFreeTurn]
	writeBin(t, ft.clientBin, "ft")
	if err := s.Uninstall("wdtt"); err != nil {
		t.Fatalf("повторное удаление обязано быть идемпотентным: %v", err)
	}
	if _, err := os.Stat(ft.clientBin); err != nil {
		t.Fatalf("удалён бинарь чужой подсистемы: %v", err)
	}
}

func TestUninstallRefusedWhileInstancesExist(t *testing.T) {
	// Снятый из-под живой записи бинарь оставил бы инстанс, который нечем
	// запустить, — гейт считает и ВЫКЛЮЧЕННЫЕ.
	s := newTestService(t, Deps{InstanceCount: countOf(map[Subsystem]int{SubsystemWdtt: 2})})
	sub := s.subs[SubsystemWdtt]
	writeBin(t, sub.clientBin, "client")

	err := s.Uninstall("wdtt")
	if !errors.Is(err, ErrInstancesExist) {
		t.Fatalf("err = %v, ждали ErrInstancesExist", err)
	}
	if _, statErr := os.Stat(sub.clientBin); statErr != nil {
		t.Fatalf("бинарь снесён вопреки гейту: %v", statErr)
	}
	// Соседняя подсистема своим гейтом не заперта.
	if err := s.Uninstall("freeturn"); err != nil {
		t.Fatalf("freeturn: %v", err)
	}
}

func TestUninstallRefusedWithoutInstanceCounter(t *testing.T) {
	// Fail-closed: без счётчика гейт не проверить, и удаление не пускается.
	s := newTestService(t, Deps{})
	sub := s.subs[SubsystemWdtt]
	writeBin(t, sub.clientBin, "client")
	if err := s.Uninstall("wdtt"); err == nil {
		t.Fatal("без счётчика инстансов удаление обязано отказать")
	}
	if _, err := os.Stat(sub.clientBin); err != nil {
		t.Fatalf("бинарь снесён без гейта: %v", err)
	}
}

func TestStatusCarriesInstanceCount(t *testing.T) {
	s := newTestService(t, Deps{InstanceCount: countOf(map[Subsystem]int{SubsystemWdtt: 3})})
	st, err := s.Status("wdtt")
	if err != nil {
		t.Fatal(err)
	}
	if st.Instances != 3 {
		t.Fatalf("Instances = %d, ждали 3: по нему фронт гасит «Удалить»", st.Instances)
	}
	ft, err := s.Status("freeturn")
	if err != nil {
		t.Fatal(err)
	}
	if ft.Instances != 0 {
		t.Fatalf("freeturn: Instances = %d, ждали 0", ft.Instances)
	}
}

func TestServeUninstall(t *testing.T) {
	post := func(s *Service, body string) *httptest.ResponseRecorder {
		rr := httptest.NewRecorder()
		s.ServeUninstall(rr, httptest.NewRequest(http.MethodPost, "/proxyrt/install/uninstall",
			strings.NewReader(body)))
		return rr
	}

	t.Run("отказ 409, пока есть инстансы", func(t *testing.T) {
		s := newTestService(t, Deps{InstanceCount: countOf(map[Subsystem]int{SubsystemWdtt: 1})})
		rr := post(s, `{"subsystem":"wdtt"}`)
		if rr.Code != http.StatusConflict {
			t.Fatalf("код %d, ждали 409", rr.Code)
		}
		var env struct {
			Code string `json:"code"`
		}
		if err := json.Unmarshal(rr.Body.Bytes(), &env); err != nil {
			t.Fatal(err)
		}
		if env.Code != "PROXY_INSTANCES_EXIST" {
			t.Fatalf("код ошибки %q", env.Code)
		}
	})

	t.Run("успех", func(t *testing.T) {
		s := newTestService(t, Deps{InstanceCount: countOf(nil)})
		writeBin(t, s.subs[SubsystemWdtt].clientBin, "client")
		rr := post(s, `{"subsystem":"wdtt"}`)
		if rr.Code != http.StatusOK {
			t.Fatalf("код %d, тело %s", rr.Code, rr.Body.String())
		}
		if _, err := os.Stat(s.subs[SubsystemWdtt].clientBin); !os.IsNotExist(err) {
			t.Fatalf("бинарь не удалён: %v", err)
		}
	})

	t.Run("неизвестная подсистема", func(t *testing.T) {
		s := newTestService(t, Deps{InstanceCount: countOf(nil)})
		if rr := post(s, `{"subsystem":"чужая"}`); rr.Code != http.StatusBadRequest {
			t.Fatalf("код %d, ждали 400", rr.Code)
		}
	})

	t.Run("GET не принимается", func(t *testing.T) {
		s := newTestService(t, Deps{InstanceCount: countOf(nil)})
		rr := httptest.NewRecorder()
		s.ServeUninstall(rr, httptest.NewRequest(http.MethodGet, "/proxyrt/install/uninstall", nil))
		if rr.Code != http.StatusMethodNotAllowed {
			t.Fatalf("код %d, ждали 405", rr.Code)
		}
	})
}

func TestUninstallRefusedWhenCountUnknown(t *testing.T) {
	// Отказ чтения записей — не ноль: без ответа гейт закрывается, иначе на
	// сбое диска бинари снеслись бы из-под живых инстансов.
	s := newTestService(t, Deps{
		InstanceCount: func(Subsystem) (int, error) { return 0, errors.New("диск не читается") },
	})
	sub := s.subs[SubsystemWdtt]
	writeBin(t, sub.clientBin, "client")
	if err := s.Uninstall("wdtt"); err == nil {
		t.Fatal("неизвестное число инстансов обязано закрывать удаление")
	}
	if _, err := os.Stat(sub.clientBin); err != nil {
		t.Fatalf("бинарь снесён при неизвестном числе инстансов: %v", err)
	}
}

func TestUninstallRefusedWhileInstalling(t *testing.T) {
	// Флаг занятости взводится на ВСЁ время сноса: иначе установка, начатая
	// между гейтом и os.Remove, теряла бы только что активированный бинарь.
	s := newTestService(t, Deps{InstanceCount: countOf(nil)})
	sub := s.subs[SubsystemWdtt]
	writeBin(t, sub.clientBin, "client")
	sub.installMu.Lock()
	sub.installing = true
	sub.installMu.Unlock()

	if err := s.Uninstall("wdtt"); err == nil {
		t.Fatal("удаление во время установки обязано отказать")
	}
	if _, err := os.Stat(sub.clientBin); err != nil {
		t.Fatalf("бинарь снесён во время установки: %v", err)
	}
}

func TestUninstallReleasesInstallingFlag(t *testing.T) {
	// Снятый флаг: иначе первая же деинсталляция запирала бы установку до
	// перезапуска демона.
	s := newTestService(t, Deps{InstanceCount: countOf(nil)})
	if err := s.Uninstall("wdtt"); err != nil {
		t.Fatal(err)
	}
	if s.subs[SubsystemWdtt].isInstalling() {
		t.Fatal("флаг занятости не снят после удаления")
	}
	// И отказ по гейту флаг тоже не оставляет взведённым.
	s2 := newTestService(t, Deps{InstanceCount: countOf(map[Subsystem]int{SubsystemWdtt: 1})})
	_ = s2.Uninstall("wdtt")
	if s2.subs[SubsystemWdtt].isInstalling() {
		t.Fatal("флаг занятости не снят после отказа гейта")
	}
}
