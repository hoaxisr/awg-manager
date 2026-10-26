package router

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// F731: группы с повтором члена, записанные прошлой версией панели, после
// обновления sing-box валили бы старт — миграция сворачивает повторы во всех
// слотах, кроме пользовательского.
func TestMigrateDuplicateGroupMembers(t *testing.T) {
	dir := t.TempDir()
	for _, sub := range []string{"disabled", "pending"} {
		if err := os.MkdirAll(filepath.Join(dir, sub), 0755); err != nil {
			t.Fatal(err)
		}
	}

	dup := `{"outbounds":[
	  {"type":"selector","tag":"sel","outbounds":["a","b","a"],"default":"b","x_unknown":1},
	  {"type":"urltest","tag":"ut","outbounds":["b","b"]},
	  {"type":"fallback","tag":"fb","outbounds":["c","a","c","b"]},
	  {"type":"direct","tag":"d"}
	]}`
	clean := `{"outbounds":[{"type":"selector","tag":"sel","outbounds":["a","b"]}]}`

	files := map[string]string{
		"20-router.json":          dup,
		"disabled/21-fakeip.json": dup,
		"pending/20-router.json":  dup,
		"13-subscriptions.json":   clean,
		userSlotFilename():        dup,
	}
	for name, body := range files {
		if err := os.WriteFile(filepath.Join(dir, name), []byte(body), 0644); err != nil {
			t.Fatal(err)
		}
	}

	changed, err := MigrateDuplicateGroupMembers(dir)
	if err != nil {
		t.Fatalf("MigrateDuplicateGroupMembers: %v", err)
	}
	if !changed {
		t.Fatal("changed = false, want true")
	}

	want := map[string]string{"sel": "a,b", "ut": "b", "fb": "c,a,b"}
	for _, name := range []string{"20-router.json", "disabled/21-fakeip.json", "pending/20-router.json"} {
		raw, err := os.ReadFile(filepath.Join(dir, name))
		if err != nil {
			t.Fatal(err)
		}
		var root struct {
			Outbounds []map[string]any `json:"outbounds"`
		}
		if err := json.Unmarshal(raw, &root); err != nil {
			t.Fatalf("%s: %v", name, err)
		}
		for _, ob := range root.Outbounds {
			tag, _ := ob["tag"].(string)
			w, ok := want[tag]
			if !ok {
				continue
			}
			var got []string
			for _, m := range ob["outbounds"].([]any) {
				got = append(got, m.(string))
			}
			if strings.Join(got, ",") != w {
				t.Errorf("%s/%s: members = %v, want %s", name, tag, got, w)
			}
		}
		if root.Outbounds[0]["x_unknown"] == nil || root.Outbounds[0]["default"] != "b" {
			t.Errorf("%s: потеряны поля группы: %+v", name, root.Outbounds[0])
		}
	}

	for _, name := range []string{"13-subscriptions.json", userSlotFilename()} {
		raw, err := os.ReadFile(filepath.Join(dir, name))
		if err != nil {
			t.Fatal(err)
		}
		if string(raw) != files[name] {
			t.Errorf("%s не должен переписываться:\n%s", name, raw)
		}
	}

	if changed, err := MigrateDuplicateGroupMembers(dir); err != nil || changed {
		t.Errorf("повторный прогон: changed=%v err=%v, want false/nil", changed, err)
	}
}
