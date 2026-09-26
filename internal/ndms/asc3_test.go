package ndms

import (
	"encoding/json"
	"reflect"
	"slices"
	"strings"
	"testing"
)

// Список ключей 3.x обязан совпадать с тегами ASCParamsAWG3 сверх
// ASCParamsExtended: иначе KeepASC3/ASC3Fields теряли бы новое поле молча.
func TestASC3KeysMatchStruct(t *testing.T) {
	typ := reflect.TypeOf(ASCParamsAWG3{})
	var tags []string
	for i := range typ.NumField() {
		if f := typ.Field(i); !f.Anonymous {
			tags = append(tags, strings.Split(f.Tag.Get("json"), ",")[0])
		}
	}
	if !slices.Equal(tags, ASC3Keys) {
		t.Fatalf("теги %v\nсписок %v", tags, ASC3Keys)
	}
}

func TestKeepASC3(t *testing.T) {
	current := map[string]json.RawMessage{"header-protection-key": json.RawMessage(`"K"`), "random-trailers": json.RawMessage(`1`)}
	for _, tc := range []struct {
		name, in string
		cur      map[string]json.RawMessage
		want     map[string]any
	}{
		{"дополняет запись 2.0", `{"jc":5}`, current, map[string]any{"jc": 5.0, "header-protection-key": "K", "random-trailers": 1.0}},
		{"запись с ключом 3.x не трогает", `{"jc":5,"random-trailers":0}`, current, map[string]any{"jc": 5.0, "random-trailers": 0.0}},
		{"без 3.x на интерфейсе — как есть", `{"jc":5}`, nil, map[string]any{"jc": 5.0}},
	} {
		raw, err := KeepASC3(json.RawMessage(tc.in), tc.cur)
		var got map[string]any
		if err != nil || json.Unmarshal(raw, &got) != nil || !reflect.DeepEqual(got, tc.want) {
			t.Errorf("%s: %s %v", tc.name, raw, err)
		}
	}
}

// Тело не-объект (null, массив) — ошибка, а не паника на nil-map.
func TestKeepASC3_NotObject(t *testing.T) {
	current := map[string]json.RawMessage{"random-trailers": json.RawMessage(`1`)}
	for _, in := range []string{`null`, `[1]`, `"x"`} {
		if _, err := KeepASC3(json.RawMessage(in), current); err == nil {
			t.Errorf("%s: ошибки нет", in)
		}
	}
}
