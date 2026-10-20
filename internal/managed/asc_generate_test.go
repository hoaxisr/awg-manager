package managed

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"math/rand"
	"strings"
	"testing"

	"github.com/hoaxisr/awg-manager/internal/ndms"
)

// Сервер 3.1 (#688): полный набор ASC3Keys (неполный прошивка молча
// игнорирует), каждый S ≥ 12 (иначе прошивка отказывает при HP), H = 1..4,
// HP — 32 байта base64, хвосты включены.
func TestGenerateASC31ParamsRaw(t *testing.T) {
	key := make([]byte, 32)
	for i := range key {
		key[i] = byte(i)
	}
	for seed := int64(0); seed < 200; seed++ {
		raw, err := generateASC31ParamsRaw(rand.NewSource(seed), key)
		if err != nil {
			t.Fatal(err)
		}
		var obj map[string]any
		if err := json.Unmarshal(raw, &obj); err != nil {
			t.Fatal(err)
		}
		for _, k := range ndms.ASC3Keys {
			if _, ok := obj[k]; !ok {
				t.Fatalf("seed %d: нет ключа %s", seed, k)
			}
		}
		for _, k := range []string{"s1", "s2", "s3", "s4"} {
			if v := obj[k].(float64); v < 12 {
				t.Fatalf("seed %d: %s=%v < 12", seed, k, v)
			}
		}
		for i, k := range []string{"h1", "h2", "h3", "h4"} {
			if obj[k] != fmt.Sprint(i+1) {
				t.Fatalf("seed %d: %s=%v", seed, k, obj[k])
			}
		}
		if obj["jc"].(float64) < 1 || obj["jmax"].(float64) <= obj["jmin"].(float64) {
			t.Fatalf("seed %d: junk %v/%v/%v", seed, obj["jc"], obj["jmin"], obj["jmax"])
		}
		hp, err := base64.StdEncoding.DecodeString(obj["header-protection-key"].(string))
		if err != nil || len(hp) != 32 || hp[31] != 31 {
			t.Fatalf("seed %d: header-protection-key %v (%v)", seed, obj["header-protection-key"], err)
		}
		if obj["random-trailers"].(float64) != 1 || obj["disable-cookies"].(float64) != 0 {
			t.Fatalf("seed %d: флаги %v/%v", seed, obj["random-trailers"], obj["disable-cookies"])
		}
	}
}

func ascPostOf(posts []map[string]interface{}, iface string) map[string]interface{} {
	for _, p := range posts {
		intf, _ := p["interface"].(map[string]interface{})
		cfg, _ := intf[iface].(map[string]interface{})
		wg, _ := cfg["wireguard"].(map[string]interface{})
		if asc, ok := wg["asc"].(map[string]interface{}); ok {
			return asc
		}
	}
	return nil
}

func TestCreate_AWG31SendsFullASC3(t *testing.T) {
	svc, _, _ := newCreateTestService(t)
	svc.supportsASC3 = func() bool { return true }
	server, err := svc.Create(context.Background(), CreateServerRequest{
		Address: "10.51.0.1", Mask: "24", ListenPort: 52051, Mode: ModeAWG31,
	})
	if err != nil {
		t.Fatalf("Create: %v", err)
	}
	asc := ascPostOf(svc.transport.(*recordingPoster).posts, server.InterfaceName)
	if asc == nil {
		t.Fatal("ASC не записан")
	}
	for _, k := range ndms.ASC3Keys {
		if _, ok := asc[k]; !ok {
			t.Fatalf("в записи ASC нет %s: %v", k, asc)
		}
	}
	if asc["header-protection-key"] == "" || fmt.Sprint(asc["random-trailers"]) != "1" {
		t.Fatalf("запись ASC не 3.1: %v", asc)
	}
}

// Режим 2.0 (по умолчанию) ключей 3.x не шлёт — как до #688.
func TestCreate_DefaultModeSendsNoASC3(t *testing.T) {
	svc, _, _ := newCreateTestService(t)
	svc.supportsASC3 = func() bool { return true }
	server, err := svc.Create(context.Background(), CreateServerRequest{
		Address: "10.52.0.1", Mask: "24", ListenPort: 52052,
	})
	if err != nil {
		t.Fatalf("Create: %v", err)
	}
	asc := ascPostOf(svc.transport.(*recordingPoster).posts, server.InterfaceName)
	for _, k := range ndms.ASC3Keys {
		if _, ok := asc[k]; ok {
			t.Fatalf("режим 2.0 шлёт %s: %v", k, asc)
		}
	}
}

// Отказ — до любого обращения к роутеру.
func TestCreate_ModeRejectedBeforeRCI(t *testing.T) {
	no := false
	for name, tc := range map[string]struct {
		req     CreateServerRequest
		asc3    bool
		wantErr error
		wantMsg string
	}{
		"3.1 без ASC3":      {CreateServerRequest{Mode: ModeAWG31}, false, ErrASC3Unsupported, ""},
		"3.0":               {CreateServerRequest{Mode: "3.0"}, true, nil, "неизвестный режим"},
		"3.1 без генерации": {CreateServerRequest{Mode: ModeAWG31, GenerateASC: &no}, true, nil, "только со сгенерированным ASC"},
	} {
		t.Run(name, func(t *testing.T) {
			svc, _, _ := newCreateTestService(t)
			svc.supportsASC3 = func() bool { return tc.asc3 }
			tc.req.Address, tc.req.Mask, tc.req.ListenPort = "10.53.0.1", "24", 52053
			_, err := svc.Create(context.Background(), tc.req)
			if err == nil || (tc.wantErr != nil && !errors.Is(err, tc.wantErr)) {
				t.Fatalf("err = %v, want %v", err, tc.wantErr)
			}
			if tc.wantErr == nil && !strings.Contains(err.Error(), tc.wantMsg) {
				t.Fatalf("ошибка не о режиме: %v", err)
			}
			if posts := svc.transport.(*recordingPoster).posts; len(posts) != 0 {
				t.Fatalf("до отказа ушло в RCI: %v", posts)
			}
		})
	}
}

// Прошивка неполный набор 3.x молча игнорирует (ответ «set ASC parameters»,
// на интерфейсе 2.0): сверка обязана это увидеть, иначе сервер 3.1 молча
// создаётся сервером 2.0 и ни один клиент к нему не подключится (F682).
func TestCreate_AWG31SilentlyIgnoredIsNotApplied(t *testing.T) {
	svc, store, getter := newCreateTestService(t)
	svc.supportsASC3 = func() bool { return true }
	poster := svc.transport.(*recordingPoster)
	poster.onPost = func(p map[string]interface{}) {
		getter.applyPost(p)
		getter.mu.Lock()
		for _, st := range getter.asc {
			for _, k := range ndms.ASC3Keys {
				delete(st, k)
			}
		}
		getter.mu.Unlock()
	}
	server, err := svc.Create(context.Background(), CreateServerRequest{
		Address: "10.54.0.1", Mask: "24", ListenPort: 52054, Mode: ModeAWG31,
	})
	if !errors.Is(err, ErrASCNotApplied) || server != nil {
		t.Fatalf("Create: сервер %v, err %v; want отказ ErrASCNotApplied", server, err)
	}
	if len(store.GetManagedServers()) != 0 {
		t.Fatal("сервер сохранён, хотя ASC 3.1 не применился")
	}
}

// Бэкап → восстановление сервера 3.1 (F682): снимок ASC несёт ключи 3.x, и
// восстановление пишет их полным набором; на прошивке без ASC 3.x — конфликт
// до любого RCI, а не сервер 2.0, к которому не подключится ни один клиент.
func TestRestore_AWG31(t *testing.T) {
	src, _, _ := newCreateTestService(t)
	src.supportsASC3 = func() bool { return true }
	server, err := src.Create(context.Background(), CreateServerRequest{
		Address: "10.57.0.1", Mask: "24", ListenPort: 52057, Mode: ModeAWG31,
	})
	if err != nil {
		t.Fatalf("Create: %v", err)
	}
	snap, err := src.GetASCParams(context.Background(), server.InterfaceName)
	if err != nil || !hasASC3(snap) {
		t.Fatalf("снимок ASC без 3.x: %s (%v)", snap, err)
	}
	export := ManagedServerExport{
		InterfaceName: "Wireguard0", Address: "10.58.0.1", Mask: "255.255.255.0", ListenPort: 52058,
		PrivateKey: validPrivateKey(1), Policy: "none", ASC: snap,
	}

	for _, asc3 := range []bool{true, false} {
		dst, _, _ := newCreateTestService(t)
		dst.supportsASC3 = func() bool { return asc3 }
		out := dst.Restore(context.Background(), []ManagedServerExport{export}, RestoreOptions{})
		posts := dst.transport.(*recordingPoster).posts
		if !asc3 {
			if len(out) != 1 || out[0].Action != "conflict" || len(posts) != 0 {
				t.Fatalf("без ASC3: %+v, постов %d", out, len(posts))
			}
			continue
		}
		if len(out) != 1 || out[0].Action != "created" {
			t.Fatalf("восстановление: %+v", out)
		}
		asc := ascPostOf(posts, "Wireguard0")
		for _, k := range ndms.ASC3Keys {
			if _, ok := asc[k]; !ok {
				t.Fatalf("восстановление не записало %s: %v", k, asc)
			}
		}
	}
}
