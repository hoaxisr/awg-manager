package realityserver

import (
	"fmt"
	"net"
	"os"
	"os/exec"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/hoaxisr/awg-manager/internal/testutil"
)

// TestXrayInterop: настоящий клиент Xray через наш вход REALITY и C-демон до
// внешнего сайта. Необязательный: нужен бинарь Xray (XRAY_BIN) и интернет —
// dest настоящий, чтобы ловить то, чего не видно на поддельном (размер
// сертификата, отпечаток ClientHello свежих клиентов).
func TestXrayInterop(t *testing.T) {
	xray := os.Getenv("XRAY_BIN")
	if xray == "" {
		t.Skip("XRAY_BIN unset")
	}
	c, _ := startC(t)
	for _, tc := range []struct{ transport, stream string }{
		{TransportTCP, `"network":"tcp"`},
		{TransportXHTTP, `"network":"xhttp","xhttpSettings":{"path":"/rx-test","mode":"stream-one"}`},
	} {
		t.Run(tc.transport, func(t *testing.T) {
			priv, pub, _ := GenerateKeyPair()
			sid, _ := GenerateShortID()
			srv := New()
			if err := srv.Apply(Config{ListenAddr: "127.0.0.1:0", Transport: tc.transport, Dest: "www.microsoft.com:443",
				PrivateKey: priv, ShortIDs: []string{sid}, XHTTPPath: "/rx-test", Upstream: c.Addr()}); err != nil {
				t.Fatal(err)
			}
			defer srv.Stop()
			_, port, _ := net.SplitHostPort(srv.Addr())
			socks := testutil.FreeAddr(t)
			_, socksPort, _ := net.SplitHostPort(socks)
			cfg := fmt.Sprintf(`{"inbounds":[{"listen":"127.0.0.1","port":%s,"protocol":"socks"}],
"outbounds":[{"protocol":"vless","settings":{"vnext":[{"address":"127.0.0.1","port":%s,"users":[{"id":"%s","encryption":"none"}]}]},
"streamSettings":{%s,"security":"reality","realitySettings":{"serverName":"www.microsoft.com","fingerprint":"chrome","publicKey":"%s","shortId":"%s"}}}]}`,
				socksPort, port, testUUID, tc.stream, pub, sid)
			f := t.TempDir() + "/xray.json"
			if err := os.WriteFile(f, []byte(cfg), 0o644); err != nil {
				t.Fatal(err)
			}
			cmd := exec.Command(xray, "run", "-c", f)
			if err := cmd.Start(); err != nil {
				t.Fatal(err)
			}
			defer func() { _ = cmd.Process.Kill(); _ = cmd.Wait() }()

			deadline := time.Now().Add(20 * time.Second) // первая проба записей dest идёт до 5 с
			for {
				out, err := exec.Command("curl", "-s", "-m", "10", "--socks5-hostname", socks,
					"https://www.cloudflare.com/cdn-cgi/trace").Output()
				if err == nil && strings.Contains(string(out), "ip=") {
					return
				}
				if time.Now().After(deadline) {
					t.Fatalf("no traffic through REALITY/%s: %v %q (active conns %s)", tc.transport, err, out, strconv.Itoa(srv.Status().ActiveConns))
				}
				time.Sleep(time.Second)
			}
		})
	}
}
