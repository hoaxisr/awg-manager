package firc

import (
	"reflect"
	"strings"
	"testing"

	"github.com/hoaxisr/awg-manager/internal/storage"
)

// Схема снимка разбирается строго (DisallowUnknownFields): поле настроек
// роутера, которого в ней нет, роняет проверку снимка у пользователя, задавшего
// это поле (так было с globalOutbound/keenDNSCloudOutbound). Новое поле
// SingboxRouterSettings обязано попасть и в схему.
func TestSnapshotRouterSchemaCoversStorage(t *testing.T) {
	tags := func(typ reflect.Type) map[string]bool {
		out := map[string]bool{}
		for i := 0; i < typ.NumField(); i++ {
			if tag := strings.Split(typ.Field(i).Tag.Get("json"), ",")[0]; tag != "" && tag != "-" {
				out[tag] = true
			}
		}
		return out
	}
	schema := tags(reflect.TypeOf(snapshotSettingsRouterSchema{}))
	for tag := range tags(reflect.TypeOf(storage.SingboxRouterSettings{})) {
		if !schema[tag] {
			t.Errorf("snapshotSettingsRouterSchema lacks %q from storage.SingboxRouterSettings", tag)
		}
	}
}
