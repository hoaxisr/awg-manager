// Package common содержит хелперы, общие для udprelay и tcpfwd
// (TURN-dial + создание obf-кодека). Два режима прокси по-разному компонуют DTLS
// и rtpopus, поэтому полная абстракция Engine/Handler намеренно не вводится -
// пакет собирает только действительно идентичный код.
package common

import (
	"context"
	"errors"
	"fmt"
	"net"

	"github.com/samosvalishe/free-turn-proxy/internal/transport/turndial"
	"github.com/samosvalishe/free-turn-proxy/internal/wire"
)

// GetCredsFunc разрешает TURN-реквизиты для streamID. Реализуется provider'ом
// (см. internal/provider): provider держит идентификатор сессии (link/room/key)
// внутри, pipeline передаёт только streamID. rawURLs - кандидаты host:port в
// порядке предпочтения.
type GetCredsFunc func(ctx context.Context, streamID int) (user, pass string, rawURLs []string, err error)

// DialTURN получает реквизиты и открывает TURN-поток, пробуя кандидатов по
// очереди: если allocate на первом не проходит (DPI-дроп/RST на relay-IP),
// берёт следующий. Возвращает первый успешный Stream. Вызывающий отвечает за
// закрытие потока и политику retry при auth-ошибке (udprelay) или перезапуска
// сессии (tcpfwd).
func DialTURN(ctx context.Context, host, port string, udp bool, peer *net.UDPAddr, streamID int, getCreds GetCredsFunc) (*turndial.Stream, error) {
	user, pass, rawURLs, err := getCreds(ctx, streamID)
	if err != nil {
		return nil, fmt.Errorf("get TURN creds: %w", err)
	}
	if len(rawURLs) == 0 {
		return nil, fmt.Errorf("no TURN candidates")
	}
	// HostOverride (-turn) принудительно задаёт host -> все кандидаты резолвятся
	// в одну цель, гонять их нет смысла; пробуем только первого.
	if host != "" {
		rawURLs = rawURLs[:1]
	}
	var errs []error
	for _, rawURL := range rawURLs {
		stream, derr := turndial.Open(ctx, turndial.Config{
			HostOverride: host,
			PortOverride: port,
			TransportUDP: udp,
		}, peer, user, pass, rawURL)
		if derr == nil {
			return stream, nil
		}
		errs = append(errs, fmt.Errorf("%s: %w", rawURL, derr))
		if ctx.Err() != nil {
			break
		}
	}
	return nil, fmt.Errorf("all TURN candidates failed: %w", errors.Join(errs...))
}

// NewClientObf возвращает клиентский wire.Codec для профиля obf или (nil, nil),
// если profile=none. Диспатч и валидация ключа - в wire.NewClientCodec.
func NewClientObf(profile string, key []byte) (wire.Codec, error) {
	return wire.NewClientCodec(profile, key)
}
