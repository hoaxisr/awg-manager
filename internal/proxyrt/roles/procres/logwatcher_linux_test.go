//go:build linux

package procres

import (
	"path/filepath"
	"testing"
	"time"
)

// Неподнявшийся watcher сообщает об ошибке и не держит номера пайпа:
// иначе stop() писал бы в чужие дескрипторы и закрывал бы их.
func TestLinuxLogWatcher_StartFailureReleasesPipe(t *testing.T) {
	w := newPlatformLogWatcher(filepath.Join(t.TempDir(), "нет", "x.log"), nil).(*linuxLogWatcher)
	if err := w.start(); err == nil {
		t.Fatal("start без каталога журнала: ошибки нет")
	}
	if w.stopPipe != [2]int{} {
		t.Fatalf("stopPipe = %v после отказа, want обнулён", w.stopPipe)
	}
	done := make(chan struct{})
	go func() { w.stop(); close(done) }()
	select {
	case <-done:
	case <-time.After(time.Second):
		t.Fatal("stop() завис после неудачного start")
	}
}
