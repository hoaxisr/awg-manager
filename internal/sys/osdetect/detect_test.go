package osdetect

import (
	"context"
	"encoding/json"
	"time"

	"github.com/hoaxisr/awg-manager/internal/ndms/query"
	"testing"

	"github.com/hoaxisr/awg-manager/internal/sys/ndmsinfo"
)

func TestParseRelease(t *testing.T) {
	tests := []struct {
		input string
		major int
		minor int
		patch int
		valid bool
	}{
		{"5.1.3", 5, 1, 3, true},
		{"5.0.14", 5, 0, 14, true},
		{"4.2.1", 4, 2, 1, true},
		{"5.1", 5, 1, 0, true},
		{"5.1.0-alpha3", 5, 1, 0, true},
		{"", 0, 0, 0, false},
		{"abc", 0, 0, 0, false},
		{"5", 0, 0, 0, false},
	}
	for _, tt := range tests {
		t.Run(tt.input, func(t *testing.T) {
			v := parseRelease(tt.input)
			if v.valid != tt.valid {
				t.Fatalf("valid = %v, want %v", v.valid, tt.valid)
			}
			if !v.valid {
				return
			}
			if v.major != tt.major || v.minor != tt.minor || v.patch != tt.patch {
				t.Errorf("got %d.%d.%d, want %d.%d.%d", v.major, v.minor, v.patch, tt.major, tt.minor, tt.patch)
			}
		})
	}
}

func TestParseReleaseAtLeastLogic(t *testing.T) {
	// Test the comparison logic directly via parsedVersion
	tests := []struct {
		release      string
		major, minor int
		want         bool
	}{
		{"5.1.3", 5, 1, true},   // 5.1 >= 5.1
		{"5.1.3", 5, 0, true},   // 5.1 >= 5.0
		{"5.0.14", 5, 1, false}, // 5.0 < 5.1
		{"5.0.14", 5, 0, true},  // 5.0 >= 5.0
		{"4.2.1", 5, 0, false},  // 4.x < 5.0
		{"5.2.0", 5, 1, true},   // 5.2 >= 5.1
		{"6.0.0", 5, 1, true},   // 6.0 >= 5.1
	}
	for _, tt := range tests {
		t.Run(tt.release, func(t *testing.T) {
			v := parseRelease(tt.release)
			if !v.valid {
				t.Fatal("expected valid parse")
			}
			var got bool
			if v.major != tt.major {
				got = v.major > tt.major
			} else {
				got = v.minor >= tt.minor
			}
			if got != tt.want {
				t.Errorf("AtLeast(%d, %d) for %s = %v, want %v", tt.major, tt.minor, tt.release, got, tt.want)
			}
		})
	}
}

func TestSupportsOpkgTunRelease(t *testing.T) {
	cases := []struct {
		release string
		want    bool
	}{
		{"5.01.C.3.0-1", true},
		{"5.0", true},
		{"4.03.C.8.0-0", false}, // прошивка репортёра #768
		{"4.3", false},
		{"3.9.C.1.0-0", false},
		{"", true},      // версия неизвестна — не блокируем (fail-open)
		{"мусор", true}, // распарсить не смогли — тоже fail-open
		{"10.0.A.1.0-0", true},
	}
	for _, c := range cases {
		if got := SupportsOpkgTunRelease(c.release); got != c.want {
			t.Errorf("SupportsOpkgTunRelease(%q) = %v, ожидалось %v", c.release, got, c.want)
		}
	}
}

// Умолчание при неизвестной версии закреплено ЗДЕСЬ, в своём пакете.
// Раньше единственной защитой от отката был тест через два слоя
// (internal/storage), а сам фолбэк не проверялся ничем.
func TestGet_UnknownVersionAssumesOS5(t *testing.T) {
	ndmsinfo.Reset()
	t.Cleanup(ndmsinfo.Reset)

	if got := Get(); got != Version5 {
		t.Errorf("Get() при неизвестной версии = %q, хотели %q", got, Version5)
	}
	if !Is5() {
		t.Error("Is5() при неизвестной версии обязан быть true")
	}
	// AtLeast НЕ меняется: «не знаем» не повод утверждать конкретный минор.
	if AtLeast(5, 1) {
		t.Error("AtLeast(5,1) при неизвестной версии обязан быть false")
	}
}

// stubGetter отдаёт заданный релиз как ответ /show/version.
type stubGetter struct{ release string }

func (g stubGetter) Get(_ context.Context, _ string, dst any) error {
	return json.Unmarshal([]byte(`{"release":"`+g.release+`","ndw":{"components":"base"}}`), dst)
}
func (stubGetter) GetRaw(context.Context, string) ([]byte, error) { return nil, nil }
func (stubGetter) Post(context.Context, any) (json.RawMessage, error) {
	return nil, nil
}

// Детект по РЕАЛЬНОМУ релизу. Умолчание закреплено отдельно (выше), но само
// распознавание четвёрки держалось только транзитивно — через тест в
// internal/storage, который ветка заменила на чистую функцию. Тем самым
// коммитом, который развернул умолчание на 5.x, четвёрка осталась без
// защиты: ошибка здесь означает оператор OS5 на роутере 4.x.
func TestGet_RealReleases(t *testing.T) {
	tests := []struct {
		release string
		want    Version
	}{
		{"4.02.01.0-0", Version4x},
		{"4.03.C.1.0-1", Version4x},
		{"5.00.A.1.0-0", Version5},
		{"5.01.C.3.0-1", Version5},
		{"6.00.A.1.0-0", Version5},
	}
	for _, tt := range tests {
		t.Run(tt.release, func(t *testing.T) {
			ndmsinfo.Reset()
			t.Cleanup(ndmsinfo.Reset)
			store := query.NewSystemInfoStore(stubGetter{release: tt.release}, nil)
			if err := ndmsinfo.Init(context.Background(), store, time.Second); err != nil {
				t.Fatalf("Init: %v", err)
			}
			if got := Get(); got != tt.want {
				t.Errorf("Get() при релизе %q = %q, хотели %q", tt.release, got, tt.want)
			}
			if got := Is5(); got != (tt.want == Version5) {
				t.Errorf("Is5() = %v при релизе %q", got, tt.release)
			}
		})
	}
}
