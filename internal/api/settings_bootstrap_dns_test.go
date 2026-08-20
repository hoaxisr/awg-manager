package api

import (
	"net/http"
	"testing"
)

// Bootstrap-резолвер обязан быть доступен БЕЗ другого резолвера, поэтому
// домен в этом поле — не «менее удобно», а неработоспособно: sing-box
// будет резолвить имя тем же резолвером, который ещё не поднят (issue #770).
func TestUpdate_SingboxBootstrapDNS_RejectsNonIP(t *testing.T) {
	for _, body := range []string{
		`{"singboxBootstrapDNS":"dns.google"}`,
		`{"singboxBootstrapDNS":"8.8.8.8:53"}`,
		`{"singboxBootstrapDNS":"1.1.1.1/32"}`,
		`{"singboxBootstrapDNS":"не адрес"}`,
	} {
		h, store := newSettingsHandlerFromRaw(t, `{"schemaVersion":2}`)
		rec := postSettingsUpdate(t, h, body)
		if rec.Code != http.StatusBadRequest {
			t.Errorf("%s → status %d, want 400", body, rec.Code)
		}
		got, err := store.Get()
		if err != nil {
			t.Fatalf("Get: %v", err)
		}
		if got.SingboxBootstrapDNS != "" {
			t.Errorf("%s → сохранилось %q, ожидалось отклонение", body, got.SingboxBootstrapDNS)
		}
	}
}

func TestUpdate_SingboxBootstrapDNS_AcceptsIP(t *testing.T) {
	cases := []struct {
		body string
		want string
	}{
		{`{"singboxBootstrapDNS":"8.8.8.8"}`, "8.8.8.8"},
		{`{"singboxBootstrapDNS":"2001:4860:4860::8888"}`, "2001:4860:4860::8888"},
		// Пустая строка — снятие настройки: адрес в 00-base.json больше не
		// навязывается, ручная правка файла снова живёт.
		{`{"singboxBootstrapDNS":""}`, ""},
	}
	for _, c := range cases {
		h, store := newSettingsHandlerFromRaw(t, `{"schemaVersion":2,"singboxBootstrapDNS":"1.1.1.1"}`)
		rec := postSettingsUpdate(t, h, c.body)
		if rec.Code != http.StatusOK {
			t.Errorf("%s → status %d, want 200 (%s)", c.body, rec.Code, rec.Body.String())
			continue
		}
		got, err := store.Get()
		if err != nil {
			t.Fatalf("Get: %v", err)
		}
		if got.SingboxBootstrapDNS != c.want {
			t.Errorf("%s → SingboxBootstrapDNS = %q, want %q", c.body, got.SingboxBootstrapDNS, c.want)
		}
	}
}

// Смена адреса должна доехать до sing-box в тот же заход: без этого настройка
// вступала бы в силу только после перезапуска демона.
func TestUpdate_SingboxBootstrapDNS_AppliesOnChange(t *testing.T) {
	h, _ := newSettingsHandlerFromRaw(t, `{"schemaVersion":2,"singboxBootstrapDNS":"1.1.1.1"}`)
	var applied []string
	h.SetApplyBootstrapDNS(func(v string) error {
		applied = append(applied, v)
		return nil
	})

	if rec := postSettingsUpdate(t, h, `{"singboxBootstrapDNS":"8.8.8.8"}`); rec.Code != http.StatusOK {
		t.Fatalf("status %d: %s", rec.Code, rec.Body.String())
	}
	if len(applied) != 1 || applied[0] != "8.8.8.8" {
		t.Fatalf("applied = %#v, want [8.8.8.8]", applied)
	}

	// Сохранение без изменения адреса не должно дёргать sing-box.
	if rec := postSettingsUpdate(t, h, `{"singboxBootstrapDNS":"8.8.8.8"}`); rec.Code != http.StatusOK {
		t.Fatalf("status %d", rec.Code)
	}
	if len(applied) != 1 {
		t.Errorf("applied = %#v, повторное применение без изменения", applied)
	}
}
