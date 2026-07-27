package pingcheck

import (
	"errors"
	"strings"
	"testing"

	testingsvc "github.com/hoaxisr/awg-manager/internal/testing"
)

// Issue #629. NDMS не отдаёт латентность по каждой проверке NativeWG, поэтому
// её измеряет отдельная проба. Все её неудачи раньше схлопывались в -1 без
// причины, и UI показывал успешную проверку как «-1 ms» и красный квадрат.
// Каждая ветка обязана давать своё объяснение.
func TestLatencyFromConnectivity(t *testing.T) {
	ms := func(v int) *int { return &v }

	cases := []struct {
		name     string
		res      *testingsvc.ConnectivityResult
		err      error
		wantMS   int
		wantNote string // подстрока
	}{
		{
			name:   "измерено",
			res:    &testingsvc.ConnectivityResult{Connected: true, Latency: ms(42)},
			wantMS: 42,
		},
		{
			name:     "проба не выполнилась",
			err:      errors.New("boom"),
			wantMS:   LatencyNotAvailable,
			wantNote: "не выполнилась",
		},
		{
			name:     "пустой результат",
			wantMS:   LatencyNotAvailable,
			wantNote: "не вернула результат",
		},
		{
			name:     "связности нет — причина известна",
			res:      &testingsvc.ConnectivityResult{Connected: false, Reason: "handshake stale: 5 minutes"},
			wantMS:   LatencyNotAvailable,
			wantNote: "handshake stale: 5 minutes",
		},
		{
			name:     "метод не измеряет время",
			res:      &testingsvc.ConnectivityResult{Connected: true},
			wantMS:   LatencyNotAvailable,
			wantNote: "не измеряет время",
		},
		{
			name:     "проверка выключена",
			res:      &testingsvc.ConnectivityResult{Connected: true, Reason: "check disabled"},
			wantMS:   LatencyNotAvailable,
			wantNote: "выключена",
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			gotMS, gotNote := LatencyFromConnectivity(tc.res, tc.err)
			if gotMS != tc.wantMS {
				t.Errorf("latency = %d, want %d", gotMS, tc.wantMS)
			}
			if tc.wantNote == "" {
				if gotNote != "" {
					t.Errorf("note = %q, want empty for a measured latency", gotNote)
				}
				return
			}
			if gotNote == "" {
				t.Fatalf("note is empty, want an explanation containing %q", tc.wantNote)
			}
			if !strings.Contains(gotNote, tc.wantNote) {
				t.Errorf("note = %q, want it to mention %q", gotNote, tc.wantNote)
			}
		})
	}
}
