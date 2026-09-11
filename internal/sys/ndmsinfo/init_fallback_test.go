package ndmsinfo

import (
	"context"
	"encoding/json"
	"path/filepath"
	"sync/atomic"
	"testing"
	"time"

	"github.com/hoaxisr/awg-manager/internal/ndms/query"
)

// deadGetter — RCI, который не отвечает никогда, со счётчиком обращений.
type deadGetter struct{ calls atomic.Int32 }

func (g *deadGetter) Get(context.Context, string, any) error {
	g.calls.Add(1)
	return errNoRCI
}
func (g *deadGetter) GetRaw(context.Context, string) ([]byte, error) { return nil, errNoRCI }
func (g *deadGetter) Post(context.Context, any) (json.RawMessage, error) {
	return nil, errNoRCI
}

var errNoRCI = errNoRCIType{}

type errNoRCIType struct{}

func (errNoRCIType) Error() string { return "RCI молчит" }

// Разбор ответа ndmc проверен отдельно, но смысл второго канала — в том, что
// он ПОДКЛЮЧЁН: без вызова из Init демон остаётся без версии и (после фикса
// F197) не поднимается вовсе. Эта развилка и есть цена ветки.
func TestInit_FallsBackToNdmcWhenRCISilent(t *testing.T) {
	Reset()
	t.Cleanup(Reset)
	fakeNdmc(t, standNdmcOutput, 0)
	noComponentsXML(t)

	g := &deadGetter{}
	sysInfo := query.NewSystemInfoStore(g, nil)

	if err := Init(context.Background(), sysInfo, 10*time.Millisecond); err != nil {
		t.Fatalf("Init при живом ndmc обязан вернуть версию: %v", err)
	}
	if g.calls.Load() == 0 {
		t.Error("RCI обязан быть опрошен ДО запасного канала")
	}
	if Source() != SourceNdmc {
		t.Errorf("источник = %q, ожидали %q", Source(), SourceNdmc)
	}
	v := Get()
	if v == nil || v.Release != "5.01.C.3.0-1" {
		t.Fatalf("версия из запасного канала не доехала до store: %+v", v)
	}
	if len(v.Components) == 0 {
		t.Error("компоненты обязаны доехать вместе с релизом")
	}
}

// Молчат оба канала — Init обязан отказать, а не оставить store в состоянии
// «версия как бы есть»: по усыновлённой неправде замерзает выбор оператора.
func TestInit_FailsWhenBothChannelsSilent(t *testing.T) {
	Reset()
	t.Cleanup(Reset)
	fakeNdmc(t, "совершенно не то", 0)
	noComponentsXML(t)

	sysInfo := query.NewSystemInfoStore(&deadGetter{}, nil)

	if err := Init(context.Background(), sysInfo, 10*time.Millisecond); err == nil {
		t.Fatal("оба канала молчат — Init обязан вернуть ошибку")
	}
	if Source() != "" {
		t.Errorf("источник = %q, ожидали пустой", Source())
	}
	if Get() != nil {
		t.Error("версии нет — Get обязан вернуть nil")
	}
}

// Живой ответ RCI точнее усыновлённого: второй канал не должен перетирать
// уже загруженное значение (у стора нет ни TTL, ни инвалидации, так что
// перетёртое значение неисправимо).
func TestInit_RCIAnswerIsNotOverwrittenByNdmc(t *testing.T) {
	Reset()
	t.Cleanup(Reset)

	sysInfo := query.NewSystemInfoStore(liveGetter{}, nil)
	if err := Init(context.Background(), sysInfo, time.Second); err != nil {
		t.Fatalf("Init: %v", err)
	}
	if Source() != SourceRCI {
		t.Fatalf("источник = %q, ожидали %q", Source(), SourceRCI)
	}

	// Второй канал приносит другое значение — оно обязано быть отвергнуто.
	fakeNdmc(t, standNdmcOutput, 0)
	v, err := versionFromNdmc(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	sysInfo.Adopt(v, SourceNdmc)

	if got := Get(); got.Release != "4.03.C.1.0-1" {
		t.Errorf("значение RCI перетёрто: %q", got.Release)
	}
	if Source() != SourceRCI {
		t.Errorf("источник после отвергнутого усыновления = %q", Source())
	}
}

type liveGetter struct{}

func (liveGetter) Get(_ context.Context, _ string, dst any) error {
	return json.Unmarshal([]byte(`{"release":"4.03.C.1.0-1","ndw":{"components":"base,dns-tls"}}`), dst)
}
func (liveGetter) GetRaw(context.Context, string) ([]byte, error) { return nil, nil }
func (liveGetter) Post(context.Context, any) (json.RawMessage, error) {
	return nil, nil
}

// noComponentsXML уводит файловый канал в никуда: проверяем поведение
// служебных каналов, и наличие файла на машине разработчика роли играть
// не должно.
func noComponentsXML(t *testing.T) {
	t.Helper()
	old := componentsXMLPath
	componentsXMLPath = filepath.Join(t.TempDir(), "нет-файла.xml")
	t.Cleanup(func() { componentsXMLPath = old })
}

// Главное следствие файлового канала: старт больше не зависит от ndm. Когда
// молчат обе службы, релиз, hw_id и состав компонентов берутся из файла — то
// есть поколение ОС, гейты ASC, выбор .ko и HasComponent() решаются верно, без
// ожидания и без догадок. Канал идёт ПОСЛЕДНИМ: он страхует молчание служб, а
// не перебивает их живой ответ.
func TestInit_FileChannelGivesReleaseWithoutNDM(t *testing.T) {
	Reset()
	t.Cleanup(Reset)
	writeComponentsXML(t, standComponentsXML)
	fakeNdmc(t, "совершенно не то", 0)

	sysInfo := query.NewSystemInfoStore(&deadGetter{}, nil)
	if err := Init(context.Background(), sysInfo, 10*time.Millisecond); err != nil {
		t.Fatalf("файл отвечает — Init обязан вернуть успех, а не ошибку: %v", err)
	}
	if Source() != SourceFile {
		t.Errorf("источник = %q, ожидали %q", Source(), SourceFile)
	}
	v := Get()
	if v == nil || v.Release != "5.01.C.3.0-1" {
		t.Fatalf("релиз не доехал: %+v", v)
	}
	if v.HardwareID != "KN-1810" {
		t.Errorf("hw_id не доехал (kmod выберет не тот .ko): %q", v.HardwareID)
	}
	// Состав компонентов файл даёт, и он совпадает с ответом RCI (сверено
	// поэлементно на стенде) — значит HasComponent() работает без ndm.
	if len(v.Components) == 0 {
		t.Error("состав компонентов обязан приехать из файла")
	}
	if !HasComponent("wireguard") {
		t.Error("HasComponent по версии из файла обязан отвечать, иначе nativewg считается недоступным")
	}
}

// Частичную версию обязан перетереть первый же полный ответ ndm — иначе
// HasComponent() навсегда останется при «нет» вместо «пока не знаем».
func TestInit_FullAnswerReplacesPartial(t *testing.T) {
	Reset()
	t.Cleanup(Reset)
	writeComponentsXML(t, standComponentsXML)

	sysInfo := query.NewSystemInfoStore(liveGetter{}, nil)
	if err := Init(context.Background(), sysInfo, time.Second); err != nil {
		t.Fatalf("Init: %v", err)
	}

	if Source() != SourceRCI {
		t.Errorf("источник = %q, ожидали %q", Source(), SourceRCI)
	}
	v := Get()
	if v == nil || v.Release != "4.03.C.1.0-1" {
		t.Fatalf("ответ ndm не перетёр файловый: %+v", v)
	}
	if len(v.Components) == 0 {
		t.Error("состав компонентов обязан приехать от ndm")
	}
}

// Файл даёт релиз, но не состав компонентов — значит второй канал обязан
// доработать поверх него. Без этого ndmc становится бесполезен везде, где
// /etc/components.xml на месте, то есть практически всегда.
func TestInit_NdmcCompletesPartialFromFile(t *testing.T) {
	Reset()
	t.Cleanup(Reset)
	writeComponentsXML(t, standComponentsXML)
	fakeNdmc(t, standNdmcOutput, 0)

	sysInfo := query.NewSystemInfoStore(&deadGetter{}, nil)
	if err := Init(context.Background(), sysInfo, 10*time.Millisecond); err != nil {
		t.Fatalf("живой ndmc обязан дать полную версию: %v", err)
	}

	if Source() != SourceNdmc {
		t.Errorf("источник = %q, ожидали %q", Source(), SourceNdmc)
	}
	v := Get()
	if v == nil || len(v.Components) == 0 {
		t.Fatalf("состав компонентов не доехал из ndmc: %+v", v)
	}
}
