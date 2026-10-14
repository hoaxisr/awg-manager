package api

import (
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"testing"

	"github.com/hoaxisr/awg-manager/internal/events"
	"github.com/hoaxisr/awg-manager/internal/singbox"
	"github.com/hoaxisr/awg-manager/internal/singbox/installer"
)

// #964: отказ гейта по месту — это 409 NO_SPACE без события «installed»,
// а не 200, после которого UI показывал тост «sing-box установлен».
func TestSingboxHandler_Install_NoSpace_409(t *testing.T) {
	dir := t.TempDir()
	binary := filepath.Join(dir, "sing-box")
	op := singbox.NewOperator(singbox.OperatorDeps{Dir: dir, Binary: binary})
	inst := installer.New(binary, "test-arch", installer.BinarySpec{
		Version: "1.2.3", URL: "u", SHA256: "s", Size: 100 << 20,
	}, nil)
	inst.SetFreeDiskFn(func(string) (int64, bool) { return 1 << 20, true })
	op.SetInstaller(inst)

	bus := events.NewBus()
	_, ch, unsub := bus.Subscribe()
	defer unsub()
	h := NewSingboxHandler(op, bus, nil, nil)

	w := httptest.NewRecorder()
	h.Install(w, httptest.NewRequest(http.MethodPost, "/api/singbox/install", nil))
	if w.Code != http.StatusConflict {
		t.Fatalf("код = %d, ожидался 409: %s", w.Code, w.Body.String())
	}
	select {
	case ev := <-ch:
		t.Fatalf("опубликовано событие %+v, ожидалась тишина", ev)
	default:
	}
}
