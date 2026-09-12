package storage

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// V37 переводит на новую цель пробы ТОЛЬКО тех, кто адрес не трогал. Свой
// выбор пользователя — его решение, и молча заменить его значило бы отобрать
// настройку.
func TestMigrateToV37_ConnectivityCheckURL(t *testing.T) {
	cases := []struct {
		name   string
		stored string
		want   string
	}{
		{
			name:   "прежний дефолт заменяется",
			stored: legacyGstaticCheckURL,
			want:   DefaultConnectivityCheckURL,
		},
		{
			name:   "пусто заполняется дефолтом",
			stored: "",
			want:   DefaultConnectivityCheckURL,
		},
		{
			name:   "выбор пользователя сохраняется",
			stored: "http://example.test/probe",
			want:   "http://example.test/probe",
		},
		{
			// Отдельный случай: адрес, совпадающий с новым дефолтом, не должен
			// зависеть от порядка миграций.
			name:   "уже новый дефолт не трогается",
			stored: DefaultConnectivityCheckURL,
			want:   DefaultConnectivityCheckURL,
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			dir := t.TempDir()
			seed := map[string]any{
				"schemaVersion":        36,
				"connectivityCheckUrl": tc.stored,
			}
			data, err := json.Marshal(seed)
			if err != nil {
				t.Fatalf("сборка настроек: %v", err)
			}
			if err := os.WriteFile(filepath.Join(dir, "settings.json"), data, 0o600); err != nil {
				t.Fatalf("запись настроек: %v", err)
			}

			s, err := NewSettingsStore(dir).Load()
			if err != nil {
				t.Fatalf("загрузка: %v", err)
			}
			if s.SchemaVersion != CurrentSchemaVersion {
				t.Errorf("версия схемы = %d, want %d", s.SchemaVersion, CurrentSchemaVersion)
			}
			if s.ConnectivityCheckURL != tc.want {
				t.Errorf("адрес пробы = %q, want %q", s.ConnectivityCheckURL, tc.want)
			}
		})
	}
}

// Цель пробы по умолчанию — НЕ Google. Замер на живом туннеле Amnezia
// (стенд 2026-09-12) показал 1 ответ из 3 у gstatic против 3 из 3 у
// cp.cloudflare, при 3 из 3 у gstatic с WAN напрямую: Google режет свои
// адреса с выходов коммерческих VPN, а панель существует ради VPN-туннелей.
// Тест — страж от возврата прежней цели «за компанию» с другими правками.
func TestDefaultConnectivityCheckURL_IsNotGoogle(t *testing.T) {
	if DefaultConnectivityCheckURL == legacyGstaticCheckURL {
		t.Fatal("цель пробы вернулась на gstatic: на выходе VPN она даёт «Нет связи» при исправном туннеле")
	}
	for _, bad := range []string{"gstatic.com", "google.com", "googleapis.com"} {
		if strings.Contains(DefaultConnectivityCheckURL, bad) {
			t.Fatalf("цель пробы %q живёт на %s — с выходов коммерческих VPN такие адреса не отвечают",
				DefaultConnectivityCheckURL, bad)
		}
	}
}

// Туннель подписки рождается с TCP-пробой, обычный — как прежде. ICMP на
// выходах коммерческих VPN фильтруется (стенд 2026-09-12: 60-100% потерь
// даже до 1.1.1.1), так что с методом "icmp" премиум-туннель при включённом
// мониторинге считался бы мёртвым всегда, а при Restart=true его ещё и
// перезапускало бы по кругу.
func TestDefaultTunnelPingCheckFor_PremiumUsesTCPProbe(t *testing.T) {
	cases := []struct {
		name    string
		country string
		want    string
	}{
		{"обычный туннель", "", "icmp"},
		{"одни пробелы — тоже обычный", "   ", "icmp"},
		{"туннель подписки", "de", "http"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			pc := DefaultTunnelPingCheckFor(tc.country)
			if pc.Method != tc.want {
				t.Fatalf("метод пробы = %q, want %q", pc.Method, tc.want)
			}
			// Остальное не расходится с общим умолчанием: мониторинг
			// по-прежнему opt-in, иначе новый туннель начал бы сам себя
			// перезапускать.
			if pc.Enabled {
				t.Error("мониторинг включён по умолчанию — он обязан быть opt-in")
			}
		})
	}
}
