package subscription

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

// xrayProfile собирает элемент Xray-массива: remarks и серверы trojan.
func xrayProfile(remarks string, servers []string, extra string) string {
	obs := make([]string, 0, len(servers)+1)
	for _, srv := range servers {
		// Пароль выводится из адреса: один и тот же сервер обязан дать один
		// ключ идентичности (identityKey) в любом профиле, иначе дедупликация
		// его не увидит и проверять будет нечего.
		pw := srv[strings.LastIndex(srv, ".")+1:]
		obs = append(obs, `{"protocol":"trojan","settings":{"servers":[{"address":"`+srv+
			`","port":443,"password":"p`+pw+`"}]}}`)
	}
	if extra != "" {
		obs = append(obs, extra)
	}
	obs = append(obs, `{"protocol":"freedom","settings":{}}`)
	return `{"remarks":"` + remarks + `","outbounds":[` + strings.Join(obs, ",") + `]}`
}

// Подписки Happ/Remnawave отдают один и тот же сервер дважды: в сводном
// профиле («Авто», где у всех узлов один remarks и имя выходит «Авто #N») и
// отдельной записью с собственным именем. Дедупликация оставляет первое
// вхождение — то есть сводное — поэтому имя обязано доставаться от записи с
// меньшим профилем-источником.
func TestApplyDiff_PreciseLabelWinsOverAggregate(t *testing.T) {
	// warp — второй рабочий узел в частном профиле: он тоже делает имя
	// выдуманным («Германия #1»), но профиль всё равно меньше сводного.
	const warp = `{"protocol":"wireguard","settings":{}}`

	cases := []struct {
		name string
		body string
		want []string
	}{
		{
			name: "сводный профиль и частные",
			body: xrayProfile("Авто", []string{"198.51.100.1", "198.51.100.2"}, "") + "," +
				xrayProfile("Германия", []string{"198.51.100.1"}, "") + "," +
				xrayProfile("Япония", []string{"198.51.100.2"}, ""),
			want: []string{"Германия", "Япония"},
		},
		{
			name: "частные профили с цепочкой warp",
			body: xrayProfile("Авто", []string{"198.51.100.1", "198.51.100.2", "198.51.100.3"}, "") + "," +
				xrayProfile("Германия", []string{"198.51.100.1"}, warp) + "," +
				xrayProfile("Япония", []string{"198.51.100.2"}, warp),
			want: []string{"Германия #1", "Япония #1", "Авто #3"},
		},
		{
			name: "сводного профиля нет: порядок и имена как в теле",
			body: xrayProfile("Пара", []string{"198.51.100.1", "198.51.100.2"}, "") + "," +
				xrayProfile("Один", []string{"198.51.100.3"}, ""),
			want: []string{"Пара #1", "Пара #2", "Один"},
		},
		{
			name: "один сервер в двух частных профилях: побеждает первый",
			body: xrayProfile("Первый", []string{"198.51.100.1"}, "") + "," +
				xrayProfile("Второй", []string{"198.51.100.1"}, ""),
			want: []string{"Первый"},
		},
		{
			// Порядок профилей в теле провайдер выбирает сам: решать
			// обязан размер профиля, а не то, кто встретился первым.
			name: "сводный профиль идёт вторым",
			body: xrayProfile("Германия", []string{"198.51.100.1"}, warp) + "," +
				xrayProfile("Авто", []string{"198.51.100.1", "198.51.100.2", "198.51.100.3"}, ""),
			want: []string{"Германия #1", "Авто #2", "Авто #3"},
		},
		{
			// Профиль без remarks имени не даёт: в теге лежит «proxy» или
			// заглушка разбора, и такое «имя» не должно вытеснять «Авто #N»,
			// по которому серверы хотя бы различимы.
			name: "частные профили без remarks",
			body: xrayProfile("Авто", []string{"198.51.100.1", "198.51.100.2"}, "") + "," +
				xrayProfile("", []string{"198.51.100.1"}, "") + "," +
				xrayProfile("", []string{"198.51.100.2"}, ""),
			want: []string{"Авто #1", "Авто #2"},
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			body := []byte("[" + tc.body + "]")
			valid := partitionParsedOutbounds("test", parseSubscriptionBody(body, "application/json").Outbounds).Valid
			got := make([]string, 0, len(valid))
			for _, n := range ApplyDiff("sub", nil, valid).New {
				got = append(got, n.Out.Label)
			}
			if strings.Join(got, "|") != strings.Join(tc.want, "|") {
				t.Errorf("имена членов = %q, ожидалось %q", got, tc.want)
			}
		})
	}
}

// Превью мастера добавления дедуплицирует своим кодом (previewBody), поэтому
// правило выбора имени обязано действовать и там: иначе список серверов в
// мастере и в готовой подписке назывался бы по-разному.
func TestPreviewURL_PreciseLabelWinsOverAggregate(t *testing.T) {
	body := "[" +
		xrayProfile("Авто", []string{"198.51.100.1", "198.51.100.2"}, "") + "," +
		xrayProfile("Германия", []string{"198.51.100.1"}, "") + "," +
		xrayProfile("Япония", []string{"198.51.100.2"}, "") + "]"
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.Write([]byte(body))
	}))
	defer srv.Close()

	svc, _ := newTestService(t)
	members, err := svc.PreviewURL(context.Background(), srv.URL, nil)
	if err != nil {
		t.Fatalf("PreviewURL: %v", err)
	}
	got := make([]string, 0, len(members))
	for _, m := range members {
		got = append(got, m.Label)
	}
	if want := []string{"Германия", "Япония"}; strings.Join(got, "|") != strings.Join(want, "|") {
		t.Errorf("имена в превью = %q, ожидалось %q", got, want)
	}
}

// От дубликата берётся ТОЛЬКО имя: тело аутбаунда остаётся от победителя.
// Иначе запись из частного профиля молча подменяла бы конфиг выхода полями,
// которых нет в ключе идентичности (alpn, utls, multiplex, packet_encoding).
func TestApplyDiff_DuplicateGivesLabelNotOutbound(t *testing.T) {
	node := func(alpn string) string {
		tls := `{"security":"tls","tlsSettings":{"serverName":"e.example"` + alpn + `}}`
		return `{"protocol":"trojan","settings":{"servers":[{"address":"198.51.100.1","port":443,"password":"p1"}]},"streamSettings":` + tls + `}`
	}
	body := `[{"remarks":"Авто","outbounds":[` + node(`,"alpn":["h2"]`) + `,` +
		`{"protocol":"trojan","settings":{"servers":[{"address":"198.51.100.2","port":443,"password":"p2"}]}}]},` +
		`{"remarks":"Германия","outbounds":[` + node("") + `]}]`

	valid := partitionParsedOutbounds("test", parseSubscriptionBody([]byte(body), "application/json").Outbounds).Valid
	diff := ApplyDiff("sub", nil, valid)
	if diff.SkippedDuplicate != 1 {
		t.Fatalf("SkippedDuplicate = %d, ожидался 1 (фикстура не даёт дубликата)", diff.SkippedDuplicate)
	}
	got := diff.New[0]
	if got.Out.Label != "Германия" {
		t.Errorf("имя = %q, ожидалось %q", got.Out.Label, "Германия")
	}
	if !strings.Contains(string(got.Out.Outbound), `"h2"`) {
		t.Errorf("тело аутбаунда взято от дубликата, а не от победителя: %s", got.Out.Outbound)
	}
}
