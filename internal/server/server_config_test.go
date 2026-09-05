package server

import (
	"reflect"
	"testing"
)

// pprof на основном mux снят вместе с флагом --pprof-on-main: дампы кучи несут токены
// сессий, api-key и приватные ключи, а гарда на этих путях не было. Профилирование —
// только через --pprof-listen на loopback. Поле конфигурации не должно вернуться.
func TestConfig_NoPprofOnMain(t *testing.T) {
	if _, ok := reflect.TypeOf(Config{}).FieldByName("PprofOnMain"); ok {
		t.Fatal("Config.PprofOnMain вернулось — pprof на основном mux запрещён")
	}
}
