package deviceproxy

import (
	"testing"

	"github.com/hoaxisr/awg-manager/internal/sys/httpclient"
)

// Прямой замер снимает прокси окружения, замер через инстанс — нет (P048).
func TestIPCheckCall_DirectIgnoresEnvProxy(t *testing.T) {
	if got := ipCheckCall("https://x", "").Proxy; got != httpclient.ProxyDirect {
		t.Fatalf("прямой замер: Proxy=%v", got)
	}
	if got := ipCheckCall("https://x", "socks5h://127.0.0.1:1").Proxy; got == httpclient.ProxyDirect {
		t.Fatal("замер через инстанс не должен менять политику")
	}
}
