package vlink

import "testing"

// LabelRank решает, чьё имя останется у сервера, пришедшего в подписке
// дважды, поэтому важно, откуда имя взято, а не как оно выглядит.
func TestParseXrayBody_LabelRank(t *testing.T) {
	node := func(addr string) string {
		return `{"tag":"proxy","protocol":"trojan","settings":{"servers":[{"address":"` + addr + `","port":443,"password":"p"}]}}`
	}
	vmess := `{"protocol":"vmess","settings":{"vnext":[{"address":"198.51.100.9","port":443,"users":[{"id":"u"}]}]}}`

	cases := []struct {
		name  string
		body  string
		label string
		rank  int
	}{
		{"remarks одиночного профиля — имя провайдера", `[{"remarks":"Германия","outbounds":[` + node("198.51.100.1") + `]}]`, "Германия", 0},
		{"профиль на 2 узла — имя выведено", `[{"remarks":"Авто","outbounds":[` + node("198.51.100.1") + `,` + node("198.51.100.2") + `]}]`, "Авто #1", 2},
		{"без remarks — имени нет", `[{"remarks":"","outbounds":[` + node("198.51.100.1") + `]}]`, "proxy", LabelRankNone},
		// vmess пропускается, но размер профиля считается по исходным узлам.
		{"vmess в профиле считается его узлом", `[{"remarks":"Микс","outbounds":[` + vmess + `,` + node("198.51.100.1") + `]}]`, "Микс #2", 2},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			res := ParseXrayBody([]byte(tc.body))
			if len(res.Outbounds) == 0 {
				t.Fatalf("не разобрано ни одного узла (errors: %v)", res.Errors)
			}
			got := res.Outbounds[0]
			if got.Label != tc.label || got.LabelRank != tc.rank {
				t.Errorf("Label/LabelRank = %q/%d, ожидалось %q/%d", got.Label, got.LabelRank, tc.label, tc.rank)
			}
		})
	}
}
