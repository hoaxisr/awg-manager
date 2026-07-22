package dnsdial

import (
	"net"
	"sync"
)

var (
	dohResolverOnce     sync.Once
	dohResolverInstance *DohResolver
)

func sharedDohResolver() *DohResolver {
	dohResolverOnce.Do(func() {
		dohResolverInstance = NewDohResolver(nil)
	})
	return dohResolverInstance
}

// AppDialer возвращает net.Dialer для tls-client и других HTTP-вызывающих.
// DNS-транспорт выбирается по mode (udp | doh | auto).
func AppDialer(mode string) net.Dialer {
	return buildDialer(mode, sharedDohResolver())
}

// InstallGlobalResolver выставляет net.DefaultResolver на тот же DNS-транспорт,
// что и AppDialer - чтобы сторонние библиотеки, собирающие свой http.Client
// без нашего Dialer, тоже шли через DoH / auto-fallback, а не OS resolver.
func InstallGlobalResolver(mode string) {
	d := AppDialer(mode)
	if d.Resolver != nil {
		net.DefaultResolver = d.Resolver
	}
}
