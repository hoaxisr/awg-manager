package roles

import (
	"strconv"
	"strings"
	"testing"
)

// ofClient — валидная фикстура клиента OpenFlux.
func ofClient() OpenFluxClientConfig {
	return OpenFluxClientConfig{
		Listen:    "127.0.0.1:9020",
		Transport: OpenFluxTransportYandex,
		URL:       "https://docs.example.com/doc",
	}
}

// ofServer — валидная фикстура выходной ноды OpenFlux.
func ofServer() OpenFluxServerConfig {
	return OpenFluxServerConfig{
		Transport: OpenFluxTransportYandex,
		URL:       "https://docs.example.com/doc",
		Mode:      OpenFluxModeL4,
	}
}

func TestOpenFluxClientValidate(t *testing.T) {
	if err := ofClient().Validate(); err != nil {
		t.Fatalf("валидная фикстура обязана проходить: %v", err)
	}
	cases := []struct {
		name string
		cfg  OpenFluxClientConfig
		err  string
	}{
		{"чужой transport", func() OpenFluxClientConfig { c := ofClient(); c.Transport = "vk"; return c }(), "transport"},
		{"yandex без url", func() OpenFluxClientConfig { c := ofClient(); c.URL = ""; return c }(), "-url"},
		{"oneme без maxToken", func() OpenFluxClientConfig {
			c := ofClient()
			c.Transport = OpenFluxTransportOneme
			c.URL = ""
			return c
		}(), "maxToken"},
		{"cupsonline без url валиден", func() OpenFluxClientConfig {
			c := ofClient()
			c.Transport = OpenFluxTransportCupsOnline
			c.URL = ""
			return c
		}(), "!"},
		{"кривой codec", func() OpenFluxClientConfig { c := ofClient(); c.Codec = "zstd"; return c }(), "codec"},
		{"нелокальный listen", func() OpenFluxClientConfig { c := ofClient(); c.Listen = "0.0.0.0:9020"; return c }(), "127.0.0.1"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			err := c.cfg.Validate()
			if c.err == "!" {
				if err != nil {
					t.Fatalf("Validate = %v, ожидали успех", err)
				}
				return
			}
			if err == nil || !strings.Contains(err.Error(), c.err) {
				t.Fatalf("Validate = %v, ожидали упоминание %q", err, c.err)
			}
		})
	}
}

func TestOpenFluxServerValidate(t *testing.T) {
	if err := ofServer().Validate(); err != nil {
		t.Fatalf("валидная фикстура обязана проходить: %v", err)
	}
	cases := []struct {
		name string
		cfg  OpenFluxServerConfig
		err  string
	}{
		{"l3 без localIp", func() OpenFluxServerConfig { c := ofServer(); c.Mode = OpenFluxModeL3; return c }(), "localIp"},
		{"l3 с мусором в localIp", func() OpenFluxServerConfig {
			c := ofServer()
			c.Mode = OpenFluxModeL3
			c.LocalIP = "wan"
			return c
		}(), "localIp"},
		{"l3 с адресом валиден", func() OpenFluxServerConfig {
			c := ofServer()
			c.Mode = OpenFluxModeL3
			c.LocalIP = "203.0.113.7"
			return c
		}(), "!"},
		{"кривой mode", func() OpenFluxServerConfig { c := ofServer(); c.Mode = "l5"; return c }(), "mode"},
		{"кривой codec", func() OpenFluxServerConfig { c := ofServer(); c.Codec = "zstd"; return c }(), "codec"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			err := c.cfg.Validate()
			if c.err == "!" {
				if err != nil {
					t.Fatalf("Validate = %v, ожидали успех", err)
				}
				return
			}
			if err == nil || !strings.Contains(err.Error(), c.err) {
				t.Fatalf("Validate = %v, ожидали упоминание %q", err, c.err)
			}
		})
	}
}

// Аргументы клиента: роль, вход, сокет и канал — без пустых флагов.
func TestOpenFluxClientArgs(t *testing.T) {
	c := ofClient()
	c.EncryptionKey = "s3cret"
	c.Codec = OpenFluxCodecLegacy
	c.Debug = true
	args := OpenFluxClientArgs(c)
	want := []string{
		"-role=client", "-inbound=socks5", "-socks5=127.0.0.1:9020",
		"-transport=yandex", "-url=https://docs.example.com/doc", "-codec=legacy",
		"-encryption-key=s3cret", "-debug",
	}
	if strings.Join(args, "\x00") != strings.Join(want, "\x00") {
		t.Fatalf("args = %v, ждали %v", args, want)
	}
	// Пустые поля флагов не порождают: дефолт codec (batched) молчит.
	bare := OpenFluxClientArgs(ofClient())
	for _, a := range bare {
		if strings.HasPrefix(a, "-codec=") || strings.HasPrefix(a, "-encryption-key=") {
			t.Fatalf("пустое поле вылезло флагом: %v", bare)
		}
	}
}

// Аргументы выхода: upstream-роль exit, режим и egress только у l3.
func TestOpenFluxServerArgs(t *testing.T) {
	c := ofServer()
	c.Mode = ""
	if got := OpenFluxServerArgs(c); got[0] != "-role=exit" || got[1] != "-mode=l4" {
		t.Fatalf("args = %v — пустой режим обязан стать l4", got)
	}
	if args := OpenFluxServerArgs(c); strings.Contains(strings.Join(args, " "), "-local-ip") {
		t.Fatalf("l4 не обязан нести egress-адрес: %v", args)
	}
	c.Mode = OpenFluxModeL3
	c.LocalIP = "203.0.113.7"
	args := OpenFluxServerArgs(c)
	joined := strings.Join(args, " ")
	if !strings.Contains(joined, "-mode=l3") || !strings.Contains(joined, "-local-ip=203.0.113.7") {
		t.Fatalf("args = %v — l3 обязан нести режим и egress", args)
	}
}

// Секреты и адреса канала едят формой -имя=значение: значение, начинающееся
// с дефиса, в раздельной форме притворилось бы флагом (§5.5 п.3).
func TestOpenFluxArgsValueWithDash(t *testing.T) {
	c := ofClient()
	c.EncryptionKey = "-weird-key-start"
	args := OpenFluxClientArgs(c)
	found := false
	for _, a := range args {
		if a == "-encryption-key=-weird-key-start" {
			found = true
		}
	}
	if !found {
		t.Fatalf("секрет с дефисом обязан ехать формой =значение: %v", args)
	}
}

// SingboxRoute едет -fwmark'ом с КОНСТАНТОЙ роли: значение общее у argv и у
// OUTPUT-правила (singboxJump), разъехаться им негде.
func TestOpenFluxServerArgsSingboxRoute(t *testing.T) {
	c := ofServer()
	c.SingboxRoute = true
	args := OpenFluxServerArgs(c)
	joined := strings.Join(args, " ")
	if !strings.Contains(joined, "-fwmark="+strconv.Itoa(OpenFluxSingboxMark)) {
		t.Fatalf("args без метки sing-box: %v", args)
	}
	// В l3 тумблер запрещён Validate: иначе метка не действовала бы молча.
	c.Mode = OpenFluxModeL3
	c.LocalIP = "203.0.113.7"
	if err := c.Validate(); err == nil || !strings.Contains(err.Error(), "l4") {
		t.Fatalf("Validate = %v, ожидали отказ l3+l4", err)
	}
}
