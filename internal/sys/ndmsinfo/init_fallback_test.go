package ndmsinfo

import (
	"context"
	"encoding/json"
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
	sysInfo.Adopt(v)

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
