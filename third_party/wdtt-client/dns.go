package main

import (
	"context"
	"net"
	"strings"
	"time"
)

func setupGlobalResolver() {
	dialer := &net.Dialer{
		Timeout:   3 * time.Second,
		KeepAlive: 30 * time.Second,
	}
	yandexDNSServers := []string{"77.88.8.8:53", "77.88.8.1:53"}

	net.DefaultResolver = &net.Resolver{
		PreferGo: true,
		Dial: func(ctx context.Context, network, address string) (net.Conn, error) {
			var lastErr error
			for _, dns := range yandexDNSServers {
				conn, err := dialer.DialContext(ctx, "udp", dns)
				if err == nil {
					return conn, nil
				}
				lastErr = err
				conn, err = dialer.DialContext(ctx, "tcp", dns)
				if err == nil {
					return conn, nil
				}
				lastErr = err
			}

			address = strings.TrimSpace(address)
			if address != "" && !isYandexDNSAddress(address) {
				conn, err := dialer.DialContext(ctx, network, address)
				if err == nil {
					return conn, nil
				}
				lastErr = err
			}
			return nil, lastErr
		},
	}
}

func isYandexDNSAddress(address string) bool {
	host, _, err := net.SplitHostPort(address)
	if err != nil {
		host = address
	}
	host = strings.Trim(host, "[]")
	return host == "77.88.8.8" || host == "77.88.8.1"
}
