package subscription

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"reflect"
	"testing"

	"github.com/hoaxisr/awg-manager/internal/proxyapp/wdttlink"
	"github.com/hoaxisr/awg-manager/internal/proxyrt/instancestore"
	"github.com/hoaxisr/awg-manager/internal/proxyrt/roles"
)

// ── фейки ────────────────────────────────────────────────────────

type fakeSource struct {
	recs map[string]instancestore.Record
}

func (f *fakeSource) Get(key string) (instancestore.Record, bool) {
	r, ok := f.recs[key]
	return r, ok
}

type updateCall struct {
	Key string
	Rec instancestore.Record // запись ПОСЛЕ прогона мутатора
}

// fakeMutator прогоняет мутатор по хранимой записи и сохраняет результат
// целиком: тест сверяет СОСТАВ аргумента, а не факт вызова.
type fakeMutator struct {
	src     *fakeSource
	updates []updateCall
	fail    error
}

func (f *fakeMutator) Create(_ context.Context, rec instancestore.Record) error {
	f.src.recs[rec.Key()] = rec
	return nil
}

func (f *fakeMutator) Update(_ context.Context, key string, mutate func(*instancestore.Record) error) error {
	if f.fail != nil {
		return f.fail
	}
	rec, ok := f.src.recs[key]
	if !ok {
		return fmt.Errorf("инстанс %s не найден", key)
	}
	// Конфиг за указателем: без клонирования правка «по месту» была бы видна
	// даже при отказе, и фейк маскировал бы потерю полей.
	if rec.WdttClient != nil {
		cfg := *rec.WdttClient
		rec.WdttClient = &cfg
	}
	if err := mutate(&rec); err != nil {
		return err
	}
	f.src.recs[key] = rec
	f.updates = append(f.updates, updateCall{Key: key, Rec: rec})
	return nil
}

// fakeFetch запоминает, С ЧЕМ его позвали: тест сверяет URL подписки, а не
// факт обращения.
type fakeFetch struct {
	got []string
	res wdttlink.LinkDecodeResult
	err error
}

func (f *fakeFetch) fetch(subURL string) (wdttlink.LinkDecodeResult, error) {
	f.got = append(f.got, subURL)
	return f.res, f.err
}

const (
	clientKey = "wdtt-client:default"
	subURL    = "https://sub.example.org/_wdtt.json?token=abc"
)

// clientRecord — запись с ЗАПОЛНЕННЫМИ полями, которых обновление подписки не
// касается: страж «пересборка записи литералом» ловится сверкой целиком.
func clientRecord() instancestore.Record {
	return instancestore.Record{
		ID:        "default",
		Kind:      instancestore.KindWdttClient,
		Name:      "Германия",
		Enabled:   true,
		CreatedAt: "2026-01-02T03:04:05Z",
		Sub:       subURL,
		PeerWg:    "1.2.3.4:56000",
		PeerRaw:   "9.9.9.9:56010",
		StatsLog:  "/opt/var/log/wdtt.log",
		WdttClient: &roles.WdttClientConfig{
			Mode:      "wg",
			Listen:    "127.0.0.1:9000",
			Peer:      "1.2.3.4:56000",
			Password:  "старый",
			VKHashes:  "old",
			Workers:   9,
			DeviceID:  "dev-1",
			NdmsIface: "OpkgTun17",
			RawIface:  "opkgtun18",
		},
	}
}

func subscriptionOf(profiles ...wdttlink.ImportPayload) wdttlink.LinkDecodeResult {
	first := profiles[0]
	return wdttlink.LinkDecodeResult{
		Profile:      &first,
		Subscription: &wdttlink.SubscriptionPreview{Name: "DarkBit", SubURL: subURL, Profiles: profiles},
	}
}

func newService(t *testing.T, rec instancestore.Record, f *fakeFetch) (*Service, *fakeSource, *fakeMutator) {
	t.Helper()
	src := &fakeSource{recs: map[string]instancestore.Record{rec.Key(): rec}}
	mut := &fakeMutator{src: src}
	return New(Deps{Records: src, Mutator: mut, Fetch: f.fetch}), src, mut
}

// ── обновление ───────────────────────────────────────────────────

func TestRefresh_UpdatesRecordInPlace(t *testing.T) {
	f := &fakeFetch{res: subscriptionOf(
		wdttlink.ImportPayload{Name: "Финляндия", Peer: "5.6.7.8:56000", Password: "b", VKHashes: []string{"hb"}},
		wdttlink.ImportPayload{Name: "Германия-2", Peer: "1.2.3.4:56000", Password: "новый",
			VKHashes: []string{"h1", "h2"}, Workers: 18, Listen: "127.0.0.1:9999", DeviceID: "dev-2"},
	)}
	s, src, mut := newService(t, clientRecord(), f)

	payload, err := s.Refresh(context.Background(), clientKey)
	if err != nil {
		t.Fatal(err)
	}
	if payload.Peer != "1.2.3.4:56000" || payload.Password != "новый" {
		t.Fatalf("профиль=%+v", payload)
	}
	// Подписка перечитывается по СОХРАНЁННОМУ URL, вместе со строкой запроса:
	// в ней едет токен.
	if !reflect.DeepEqual(f.got, []string{subURL}) {
		t.Fatalf("Fetch позван с %#v", f.got)
	}

	want := clientRecord()
	want.Name = "Германия-2"
	want.WdttClient.Password = "новый"
	want.WdttClient.VKHashes = "h1,h2"
	want.WdttClient.Workers = 18
	want.WdttClient.DeviceID = "dev-2"
	// Listen подписка НЕ трогает: порт выделен менеджером, и увести его в
	// чужой значило бы разойтись с аллокатором и связанным туннелем.
	if len(mut.updates) != 1 || mut.updates[0].Key != clientKey {
		t.Fatalf("вызовы мутатора: %+v", mut.updates)
	}
	got := src.recs[clientKey]
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("запись после обновления:\n got %+v / %+v\nwant %+v / %+v",
			got, *got.WdttClient, want, *want.WdttClient)
	}
}

// Профиль без имени имя записи не трогает: пользователь мог переименовать
// инстанс сам.
func TestRefresh_KeepsNameWhenProfileHasNone(t *testing.T) {
	f := &fakeFetch{res: subscriptionOf(
		wdttlink.ImportPayload{Peer: "1.2.3.4:56000", Password: "новый"},
	)}
	s, src, _ := newService(t, clientRecord(), f)
	if _, err := s.Refresh(context.Background(), clientKey); err != nil {
		t.Fatal(err)
	}
	got := src.recs[clientKey]
	if got.Name != "Германия" {
		t.Fatalf("имя=%q", got.Name)
	}
	if got.WdttClient.Password != "новый" {
		t.Fatalf("профиль не применён вовсе: пароль=%q", got.WdttClient.Password)
	}
}

// Режим связи из профиля нормализуется: сервер понимает только wg и raw.
//
// Входы подобраны так, чтобы ОТЛИЧАТЬ нормализацию от константы: одного кейса
// мало — «всегда raw» прошёл бы его незамеченным. Поэтому каждый ожидаемый
// ответ ОТЛИЧАЕТСЯ от сохранённого режима, и в наборе есть оба исхода.
func TestRefresh_NormalizesConnMode(t *testing.T) {
	for _, tc := range []struct {
		name       string
		storedMode string
		connMode   string
		want       string
	}{
		{"верхний регистр raw", "wg", "RAW", "raw"},
		{"верхний регистр и пробелы wg", "raw", "  WG  ", "wg"},
		{"неизвестное значение — wg", "raw", "мусор", "wg"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			rec := clientRecord()
			rec.WdttClient.Mode = tc.storedMode
			f := &fakeFetch{res: subscriptionOf(
				wdttlink.ImportPayload{Peer: "1.2.3.4:56000", ConnMode: tc.connMode},
			)}
			s, src, _ := newService(t, rec, f)
			if _, err := s.Refresh(context.Background(), clientKey); err != nil {
				t.Fatal(err)
			}
			if got := src.recs[clientKey].WdttClient.Mode; got != tc.want {
				t.Fatalf("режим=%q, want %q (сохранён был %q)", got, tc.want, tc.storedMode)
			}
		})
	}
}

// Пустое поле профиля НЕ затирает сохранённое — инвариант, заявленный
// докстрокой applyProfile. Провайдер отдаёт разный набор полей, и документ без
// части полей молча откатил бы настройки клиента, ничем не пожаловавшись.
//
// В каждом кейсе пусто РОВНО ОДНО поле, а соседние заполнены: так провал
// называет виновный гейт, а не «что-то в applyProfile».
func TestRefresh_EmptyProfileFieldsDoNotOverwrite(t *testing.T) {
	// Сохранённые значения — заведомо не нулевые и не дефолтные, иначе
	// «затёрли» и «оставили» были бы неразличимы.
	const (
		storedWorkers  = 9
		storedDeviceID = "dev-1"
		storedMode     = "raw"
	)

	for _, tc := range []struct {
		name    string
		profile wdttlink.ImportPayload
	}{
		{"число воркеров", wdttlink.ImportPayload{
			Peer: "1.2.3.4:56000", Password: "новый", Workers: 0, DeviceID: "dev-2", ConnMode: "wg"}},
		{"идентификатор устройства", wdttlink.ImportPayload{
			Peer: "1.2.3.4:56000", Password: "новый", Workers: 18, DeviceID: "", ConnMode: "wg"}},
		{"режим подключения", wdttlink.ImportPayload{
			Peer: "1.2.3.4:56000", Password: "новый", Workers: 18, DeviceID: "dev-2", ConnMode: ""}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			rec := clientRecord()
			rec.WdttClient.Workers = storedWorkers
			rec.WdttClient.DeviceID = storedDeviceID
			rec.WdttClient.Mode = storedMode

			s, src, _ := newService(t, rec, &fakeFetch{res: subscriptionOf(tc.profile)})
			if _, err := s.Refresh(context.Background(), clientKey); err != nil {
				t.Fatal(err)
			}
			got := src.recs[clientKey].WdttClient

			// Профиль применён вообще: иначе «поле не затёрто» доказывало бы
			// только то, что обновление не сработало.
			if got.Password != "новый" {
				t.Fatalf("профиль не применён вовсе: пароль=%q", got.Password)
			}

			// Ожидания — литералами, а не через normalizeConnMode: считать их
			// проверяемой функцией значило бы сверять её саму с собой.
			wantWorkers, wantDeviceID, wantMode := tc.profile.Workers, tc.profile.DeviceID, "wg"
			if tc.profile.Workers == 0 {
				wantWorkers = storedWorkers
			}
			if tc.profile.DeviceID == "" {
				wantDeviceID = storedDeviceID
			}
			if tc.profile.ConnMode == "" {
				wantMode = storedMode
			}
			if got.Workers != wantWorkers {
				t.Fatalf("воркеры=%d, want %d", got.Workers, wantWorkers)
			}
			if got.DeviceID != wantDeviceID {
				t.Fatalf("идентификатор устройства=%q, want %q", got.DeviceID, wantDeviceID)
			}
			if got.Mode != wantMode {
				t.Fatalf("режим=%q, want %q", got.Mode, wantMode)
			}
		})
	}
}

// ── отказы ───────────────────────────────────────────────────────

func TestRefresh_Rejections(t *testing.T) {
	t.Run("URL подписки не сохранён", func(t *testing.T) {
		rec := clientRecord()
		rec.Sub = ""
		f := &fakeFetch{}
		s, _, mut := newService(t, rec, f)
		_, err := s.Refresh(context.Background(), clientKey)
		if err == nil || err.Error() != "URL подписки не сохранён — импортируйте HTTPS _wdtt.json ещё раз" {
			t.Fatalf("err=%v", err)
		}
		if len(f.got) != 0 || len(mut.updates) != 0 {
			t.Fatalf("сеть/мутатор тронуты: %#v %+v", f.got, mut.updates)
		}
	})

	t.Run("подписка не HTTPS", func(t *testing.T) {
		rec := clientRecord()
		rec.Sub = "wdtt://1.2.3.4"
		s, _, _ := newService(t, rec, &fakeFetch{})
		if _, err := s.Refresh(context.Background(), clientKey); err == nil {
			t.Fatal("не-HTTP(S) подписка обязана быть отвергнута")
		}
	})

	t.Run("у клиента нет peer", func(t *testing.T) {
		rec := clientRecord()
		rec.WdttClient.Peer = ""
		rec.PeerWg = ""
		f := &fakeFetch{}
		s, _, _ := newService(t, rec, f)
		_, err := s.Refresh(context.Background(), clientKey)
		if err == nil || err.Error() != "у клиента не задан peer — нечего сопоставить с подпиской" {
			t.Fatalf("err=%v", err)
		}
		if len(f.got) != 0 {
			t.Fatalf("сеть тронута: %#v", f.got)
		}
	})

	t.Run("подписка не загрузилась", func(t *testing.T) {
		f := &fakeFetch{err: errors.New("таймаут")}
		s, _, mut := newService(t, clientRecord(), f)
		_, err := s.Refresh(context.Background(), clientKey)
		if err == nil || err.Error() != "не удалось загрузить подписку: таймаут" {
			t.Fatalf("err=%v", err)
		}
		if len(mut.updates) != 0 {
			t.Fatalf("отказ загрузки дошёл до записи: %+v", mut.updates)
		}
	})

	t.Run("в подписке нет профилей", func(t *testing.T) {
		s, _, _ := newService(t, clientRecord(), &fakeFetch{})
		_, err := s.Refresh(context.Background(), clientKey)
		if err == nil || err.Error() != "подписка не содержит профилей" {
			t.Fatalf("err=%v", err)
		}
	})

	t.Run("peer не найден в подписке", func(t *testing.T) {
		f := &fakeFetch{res: subscriptionOf(
			wdttlink.ImportPayload{Peer: "5.6.7.8:56000"},
			wdttlink.ImportPayload{Peer: "7.7.7.7:56000"},
		)}
		s, _, mut := newService(t, clientRecord(), f)
		_, err := s.Refresh(context.Background(), clientKey)
		want := "peer 1.2.3.4:56000 не найден в подписке (2 профилей) — выберите другой сервер через повторный импорт"
		if err == nil || err.Error() != want {
			t.Fatalf("err=%v", err)
		}
		if len(mut.updates) != 0 {
			t.Fatalf("несовпавший профиль дошёл до записи: %+v", mut.updates)
		}
	})

	t.Run("инстанса нет", func(t *testing.T) {
		s, _, _ := newService(t, clientRecord(), &fakeFetch{})
		_, err := s.Refresh(context.Background(), "wdtt-client:нет")
		if !errors.Is(err, ErrInstanceNotFound) {
			t.Fatalf("err=%v", err)
		}
	})

	// Обновление подписки есть только у wdtt-клиента: у freeturn подписка —
	// поле конфига, её перечитывает сам процесс по -sub.
	t.Run("чужая роль", func(t *testing.T) {
		rec := instancestore.Record{ID: "default", Kind: instancestore.KindFreeTurnClient,
			Name: "FT", Sub: subURL,
			FreeTurnClient: &roles.FreeTurnClientConfig{Listen: "127.0.0.1:9000", Sub: subURL}}
		f := &fakeFetch{}
		s, _, mut := newService(t, rec, f)
		_, err := s.Refresh(context.Background(), rec.Key())
		// Текст сверяется ЦЕЛИКОМ: без явного гейта отказ пришёл бы от
		// типизированного геттера конфига — «дефект вызывающего», то есть
		// пользователю показали бы внутреннюю жалобу вместо объяснения.
		want := "инстанс freeturn-client:default: обновление подписки есть только у wdtt-клиента; " +
			"у роли freeturn-client подписка — поле конфига, её перечитывает сам процесс"
		if err == nil || err.Error() != want {
			t.Fatalf("err=%v", err)
		}
		if len(f.got) != 0 || len(mut.updates) != 0 {
			t.Fatalf("чужая роль дошла до сети/записи: %#v %+v", f.got, mut.updates)
		}
	})

	t.Run("нет проводки", func(t *testing.T) {
		if _, err := New(Deps{}).Refresh(context.Background(), clientKey); err == nil {
			t.Fatal("без источника записей обновление обязано отказать")
		}
		src := &fakeSource{recs: map[string]instancestore.Record{clientKey: clientRecord()}}
		noMut := New(Deps{Records: src, Fetch: (&fakeFetch{res: subscriptionOf(
			wdttlink.ImportPayload{Peer: "1.2.3.4:56000"})}).fetch})
		if _, err := noMut.Refresh(context.Background(), clientKey); err == nil {
			t.Fatal("без мутатора обновление обязано отказать, а не потерять профиль")
		}
	})

	t.Run("запись не сохранилась", func(t *testing.T) {
		f := &fakeFetch{res: subscriptionOf(wdttlink.ImportPayload{Peer: "1.2.3.4:56000"})}
		s, _, mut := newService(t, clientRecord(), f)
		mut.fail = errors.New("диск полон")
		if _, err := s.Refresh(context.Background(), clientKey); err == nil {
			t.Fatal("отказ записи обязан доехать до вызывающего")
		}
	})
}

// ── ручка ────────────────────────────────────────────────────────

func TestRefreshHandler_Form(t *testing.T) {
	f := &fakeFetch{res: subscriptionOf(
		wdttlink.ImportPayload{Name: "Германия-2", Peer: "1.2.3.4:56000", Password: "новый",
			VKHashes: []string{"h1"}},
	)}
	s, _, _ := newService(t, clientRecord(), f)

	rr := httptest.NewRecorder()
	s.Serve(rr, httptest.NewRequest(http.MethodPost, "/x", nil), clientKey)
	if rr.Code != http.StatusOK {
		t.Fatalf("code=%d body=%s", rr.Code, rr.Body)
	}
	var env map[string]any
	if err := json.Unmarshal(rr.Body.Bytes(), &env); err != nil {
		t.Fatal(err)
	}
	data, _ := env["data"].(map[string]any)
	if data["key"] != clientKey {
		t.Fatalf("key=%v", data["key"])
	}
	if data["message"] != "Подписка обновлена — проверьте пароль и VK-хеши, при необходимости перезапустите клиент" {
		t.Fatalf("message=%v", data["message"])
	}
	payload, _ := data["payload"].(map[string]any)
	if payload["peer"] != "1.2.3.4:56000" || payload["password"] != "новый" {
		t.Fatalf("payload=%+v", payload)
	}
	// Запись в ответ НЕ уходит: маскировка секретов живёт в ручке инстансов,
	// второй копии этого правила здесь быть не должно.
	if _, ok := data["instance"]; ok {
		t.Fatalf("ответ несёт запись инстанса: %+v", data)
	}
}

func TestRefreshHandler_Rejections(t *testing.T) {
	s, _, _ := newService(t, clientRecord(), &fakeFetch{err: errors.New("таймаут")})

	rr := httptest.NewRecorder()
	s.Serve(rr, httptest.NewRequest(http.MethodGet, "/x", nil), clientKey)
	if rr.Code != http.StatusMethodNotAllowed {
		t.Fatalf("GET code=%d", rr.Code)
	}

	rr = httptest.NewRecorder()
	s.Serve(rr, httptest.NewRequest(http.MethodPost, "/x", nil), clientKey)
	var env map[string]any
	_ = json.Unmarshal(rr.Body.Bytes(), &env)
	if env["code"] != "WDTT_SUBSCRIPTION_REFRESH_FAILED" {
		t.Fatalf("код отказа=%v", env["code"])
	}

	rr = httptest.NewRecorder()
	s.Serve(rr, httptest.NewRequest(http.MethodPost, "/x", nil), "wdtt-client:нет")
	if rr.Code != http.StatusNotFound {
		t.Fatalf("несуществующий инстанс code=%d body=%s", rr.Code, rr.Body)
	}
}
