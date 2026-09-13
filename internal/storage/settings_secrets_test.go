package storage

import (
	"encoding/json"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
)

// Снятый секрет не остаётся жить в .bak.
//
// Подтверждено на живом роутере 12.09.2026: после «забыть ключ» в settings.json
// вхождений шифротекста 0, а в settings.json.bak — 1, при живом .device-key
// рядом. То есть цепочка «чтение файлов роутера → .bak + .device-key → ключ
// подписки» была полной и воспроизводимой.
func TestSaveSettings_DroppedSecretLeavesBak(t *testing.T) {
	const secret = "fixture-secret-value-0123456789"

	cases := []struct {
		name string
		set  func(*Settings)
		drop func(*Settings)
	}{
		{
			name: "ключ подписки Amnezia",
			set:  func(s *Settings) { s.AmneziaPremiumKeyCipher = secret },
			drop: func(s *Settings) { s.AmneziaPremiumKeyCipher = "" },
		},
		{
			name: "apiKey панели",
			set:  func(s *Settings) { s.ApiKey = secret },
			drop: func(s *Settings) { s.ApiKey = "" },
		},
		{
			// Секрет в КАРТЕ: мутаторы копируют Settings поверхностно, и
			// сравнение по структуре здесь слепо — проверка обязана ловить и
			// этот случай.
			name: "приватный ключ пира в serverPeerSecrets",
			set: func(s *Settings) {
				s.ServerPeerSecrets = map[string]map[string]ServerPeerSecret{
					"srv1": {"pub1": {PrivateKey: secret}},
				}
			},
			drop: func(s *Settings) { s.ServerPeerSecrets = nil },
		},
		{
			// Секрет в СРЕЗЕ — то же соображение.
			name: "приватный ключ managed-сервера",
			set: func(s *Settings) {
				s.ManagedServers = []ManagedServer{{InterfaceName: "srv1", PrivateKey: secret}}
			},
			drop: func(s *Settings) { s.ManagedServers = nil },
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			dir := t.TempDir()
			store := NewSettingsStore(dir)
			cur, err := store.Load()
			if err != nil {
				t.Fatalf("загрузка: %v", err)
			}

			withSecret := *cur
			tc.set(&withSecret)
			if err := store.Update(func(cur *Settings) error { *cur = withSecret; return nil }); err != nil {
				t.Fatalf("запись с секретом: %v", err)
			}
			if !strings.Contains(readFile(t, filepath.Join(dir, "settings.json")), secret) {
				t.Fatal("секрет не доехал до файла — проверять нечего")
			}

			withoutSecret := withSecret
			tc.drop(&withoutSecret)
			if err := store.Update(func(cur *Settings) error { *cur = withoutSecret; return nil }); err != nil {
				t.Fatalf("запись без секрета: %v", err)
			}

			main := readFile(t, filepath.Join(dir, "settings.json"))
			if strings.Contains(main, secret) {
				t.Fatalf("секрет остался в settings.json")
			}
			bak := readFile(t, filepath.Join(dir, "settings.json.bak"))
			if strings.Contains(bak, secret) {
				t.Fatalf("секрет ПЕРЕЖИЛ удаление в settings.json.bak — F254")
			}
		})
	}
}

// Вторая запись делается ТОЛЬКО когда секрет пропал: безусловная удваивала бы
// число записей на флеш у каждой правки настроек. Проверяется по содержимому
// .bak: он обязан остаться ПРЕЖНЕЙ версией, а не копией новой.
func TestSaveSettings_BakKeepsPreviousWhenNoSecretDropped(t *testing.T) {
	dir := t.TempDir()
	store := NewSettingsStore(dir)
	cur, err := store.Load()
	if err != nil {
		t.Fatalf("загрузка: %v", err)
	}

	first := *cur
	first.ConnectivityCheckURL = "https://first.example/probe"
	if err := store.Update(func(cur *Settings) error { *cur = first; return nil }); err != nil {
		t.Fatalf("первая запись: %v", err)
	}

	second := first
	second.ConnectivityCheckURL = "https://second.example/probe"
	if err := store.Update(func(cur *Settings) error { *cur = second; return nil }); err != nil {
		t.Fatalf("вторая запись: %v", err)
	}

	bak := readFile(t, filepath.Join(dir, "settings.json.bak"))
	if !strings.Contains(bak, "https://first.example/probe") {
		t.Fatalf(".bak перестал быть прежней версией — вторая запись стала безусловной")
	}
}

// Страховка от порчи сохраняется: после снятия секрета .bak остаётся ВАЛИДНЫМ
// файлом настроек, и Load восстанавливается из него.
func TestSaveSettings_BakStillRecoversAfterSecretDrop(t *testing.T) {
	dir := t.TempDir()
	store := NewSettingsStore(dir)
	cur, err := store.Load()
	if err != nil {
		t.Fatalf("загрузка: %v", err)
	}

	withSecret := *cur
	withSecret.ApiKey = "fixture-secret-value-0123456789"
	withSecret.ConnectivityCheckURL = "https://kept.example/probe"
	if err := store.Update(func(cur *Settings) error { *cur = withSecret; return nil }); err != nil {
		t.Fatalf("запись с секретом: %v", err)
	}
	withoutSecret := withSecret
	withoutSecret.ApiKey = ""
	if err := store.Update(func(cur *Settings) error { *cur = withoutSecret; return nil }); err != nil {
		t.Fatalf("запись без секрета: %v", err)
	}

	// Портим основной файл и перечитываем магазин с нуля.
	if err := os.WriteFile(filepath.Join(dir, "settings.json"), []byte("{битый"), 0o600); err != nil {
		t.Fatalf("порча файла: %v", err)
	}
	restored, err := NewSettingsStore(dir).Load()
	if err != nil {
		t.Fatalf("восстановление из .bak не сработало: %v", err)
	}
	if restored.ConnectivityCheckURL != "https://kept.example/probe" {
		t.Fatalf("восстановлено не то: %q", restored.ConnectivityCheckURL)
	}
	if restored.ApiKey != "" {
		t.Fatalf("из .bak приехал снятый секрет: %q", restored.ApiKey)
	}
}

// Файлы настроек несут apiKey и приватные ключи ОТКРЫТЫМ текстом — читать их
// не должен никто, кроме владельца.
func TestSaveSettings_FilePermissionsAreOwnerOnly(t *testing.T) {
	dir := t.TempDir()
	store := NewSettingsStore(dir)
	cur, err := store.Load()
	if err != nil {
		t.Fatalf("загрузка: %v", err)
	}
	first := *cur
	first.ApiKey = "fixture-secret-value-0123456789"
	if err := store.Update(func(cur *Settings) error { *cur = first; return nil }); err != nil {
		t.Fatalf("первая запись: %v", err)
	}
	second := first
	second.ApiKey = ""
	if err := store.Update(func(cur *Settings) error { *cur = second; return nil }); err != nil {
		t.Fatalf("вторая запись: %v", err)
	}

	for _, name := range []string{"settings.json", "settings.json.bak"} {
		info, err := os.Stat(filepath.Join(dir, name))
		if err != nil {
			t.Fatalf("stat %s: %v", name, err)
		}
		if perm := info.Mode().Perm(); perm != SecretFilePermission {
			t.Errorf("%s: права %#o, ожидались %#o", name, perm, SecretFilePermission)
		}
	}
}

// Карантинный файл — снимок настроек целиком, то есть те же секреты. Вычистить
// их нечем (файл на то и карантинный, что не разбирается), значит остаётся
// закрыть права: он переживает перезагрузки и лежит до ручного разбора.
func TestLoadSettings_QuarantineIsOwnerOnly(t *testing.T) {
	dir := t.TempDir()
	store := NewSettingsStore(dir)
	cur, err := store.Load()
	if err != nil {
		t.Fatalf("загрузка: %v", err)
	}
	good := *cur
	good.ConnectivityCheckURL = "https://kept.example/probe"
	if err := store.Update(func(cur *Settings) error { *cur = good; return nil }); err != nil {
		t.Fatalf("запись: %v", err)
	}
	// Вторая запись нужна, чтобы появился .bak, из которого пойдёт восстановление.
	again := good
	again.ConnectivityCheckURL = "https://kept2.example/probe"
	if err := store.Update(func(cur *Settings) error { *cur = again; return nil }); err != nil {
		t.Fatalf("вторая запись: %v", err)
	}

	if err := os.WriteFile(filepath.Join(dir, "settings.json"), []byte("{битый"), 0o644); err != nil {
		t.Fatalf("порча файла: %v", err)
	}
	if _, err := NewSettingsStore(dir).Load(); err != nil {
		t.Fatalf("загрузка после порчи: %v", err)
	}

	info, err := os.Stat(filepath.Join(dir, "settings.json.corrupt"))
	if err != nil {
		t.Fatalf("карантинного файла нет: %v", err)
	}
	if perm := info.Mode().Perm(); perm != SecretFilePermission {
		t.Errorf("карантин: права %#o, ожидались %#o", perm, SecretFilePermission)
	}
}

// Страж списка секретных полей: новое секретное поле в типах обязано попасть в
// secretJSONKeys, иначе оно молча переживёт удаление в .bak — ровно тот дефект,
// который здесь и чинится.
//
// Ищем по ИМЕНИ json-тега: имя «…Key», «…Secret», «…Cipher», «…Password» и есть
// признак секрета, а перечислять пути в структуре бессмысленно — поле переезжает.
func TestSecretJSONKeys_CoverSettingsSecrets(t *testing.T) {
	src, err := os.ReadFile("types.go")
	if err != nil {
		t.Fatalf("чтение types.go: %v", err)
	}
	tagRe := regexp.MustCompile("json:\"([A-Za-z0-9_]+)")
	// Поля, чьё имя похоже на секрет, но секретом не являющиеся: публичный
	// ключ не тайна, а имя или признак тем более. Отдельно —
	// serverPeerSecrets: это КОНТЕЙНЕР, собственного значения он не несёт,
	// секрет лежит в листе privateKey, который в списке уже есть. Поиск идёт
	// по парам «поле: строка», поэтому контейнер в него не попадает вовсе.
	allowed := map[string]bool{
		"publicKey":         true,
		"serverPublicKey":   true,
		"peerPublicKey":     true,
		"apiKeyEnabled":     true,
		"headerProtection":  true,
		"serverPeerSecrets": true,
	}
	known := map[string]bool{}
	for _, k := range secretJSONKeys {
		known[k] = true
	}

	suspicious := regexp.MustCompile(`(?i)(privatekey|presharedkey|apikey|password|secret|cipher)`)
	var missing []string
	for _, m := range tagRe.FindAllSubmatch(src, -1) {
		name := string(m[1])
		if allowed[name] || known[name] || !suspicious.MatchString(name) {
			continue
		}
		missing = append(missing, name)
	}
	if len(missing) > 0 {
		t.Fatalf("секретные поля настроек не попали в secretJSONKeys: %v — они переживут удаление в .bak", missing)
	}
}

func readFile(t *testing.T, path string) string {
	t.Helper()
	b, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("чтение %s: %v", path, err)
	}
	return string(b)
}

// Форма settings.json стабильна: тест выше ищет секреты в сериализованном
// виде, поэтому маршалинг обязан их туда класть. Проверка отдельная, чтобы
// падение было адресным, если поле вдруг получит json:"-".
func TestSettings_SecretsAreSerialized(t *testing.T) {
	s := Settings{
		ApiKey:                  "a",
		AmneziaPremiumKeyCipher: "b",
		ManagedServers:          []ManagedServer{{InterfaceName: "srv", PrivateKey: "c"}},
	}
	raw, err := json.Marshal(&s)
	if err != nil {
		t.Fatalf("маршалинг: %v", err)
	}
	for _, want := range []string{"apiKey", "amneziaPremiumKeyCipher", "privateKey"} {
		if !strings.Contains(string(raw), want) {
			t.Errorf("поле %q не сериализуется — поиск секретов его не увидит", want)
		}
	}
}
