package vkauth

import (
	"context"
	"net"
	"time"

	"github.com/samosvalishe/free-turn-proxy/internal/netconn"
	"github.com/samosvalishe/free-turn-proxy/internal/provider/vk/internal/browserprofile"

	fhttp "github.com/bogdanfinn/fhttp"
	tlsclient "github.com/bogdanfinn/tls-client"
	"golang.org/x/net/proxy"
)

const (
	// clientHelloSplitAt - фоллбэк-offset разбиения, когда SNI в ClientHello не
	// распарсился. Совпадает с TURN STUN-split.
	clientHelloSplitAt = 6
	// clientHelloSplitDelay / clientHelloSplitJitter - пауза между сегментами
	// ClientHello (base + случайная добавка [0,jitter)) для антифингерпринта тайминга.
	clientHelloSplitDelay  = 20 * time.Millisecond
	clientHelloSplitJitter = 15 * time.Millisecond
)

// splitDialer оборачивает base.DialContext и дробит первый Write результирующего
// conn (TLS ClientHello) по границам внутри SNI host_name для обхода SNI-based
// DPI RST. Реализует proxy.ContextDialer - tls-client берёт его через
// WithProxyDialerFactory как прямой (не прокси) дилер.
type splitDialer struct {
	base net.Dialer
}

func (d *splitDialer) Dial(network, addr string) (net.Conn, error) {
	return d.DialContext(context.Background(), network, addr)
}

func (d *splitDialer) DialContext(ctx context.Context, network, addr string) (net.Conn, error) {
	c, err := d.base.DialContext(ctx, network, addr)
	if err != nil {
		return nil, err
	}
	return &netconn.MultiSplitWriteConn{
		Conn:            c,
		Delay:           clientHelloSplitDelay,
		Jitter:          clientHelloSplitJitter,
		FallbackSplitAt: clientHelloSplitAt,
	}, nil
}

func (c *Client) newTLSClient(profile browserprofile.Profile, jar tlsclient.CookieJar) (tlsclient.HttpClient, error) {
	return tlsclient.NewHttpClient(tlsclient.NewNoopLogger(),
		tlsclient.WithTimeoutSeconds(20),
		tlsclient.WithClientProfile(profile.ClientProfile()),
		tlsclient.WithCookieJar(jar),
		tlsclient.WithProxyDialerFactory(func(_ string, timeout time.Duration, localAddr *net.TCPAddr, _ fhttp.Header, _ tlsclient.Logger) (proxy.ContextDialer, error) {
			base := c.dialer
			base.Timeout = timeout
			if localAddr != nil {
				base.LocalAddr = localAddr
			}
			return &splitDialer{base: base}, nil
		}),
	)
}
