// Package dtlsdial оборачивает настройку pion-dtls клиента (self-signed cert, EMS,
// AES-128-GCM, send-only CID) плюс опциональный конкурентный gate на handshake.
// Используется UDP и VLESS pipeline'ами клиента.
package dtlsdial

import (
	"context"
	"crypto/tls"
	"net"
	"time"

	"github.com/pion/dtls/v3"
	"github.com/pion/dtls/v3/pkg/crypto/selfsign"
)

func GenerateSelfSignedCert() (tls.Certificate, error) {
	return selfsign.GenerateSelfSigned()
}

// Dialer конфигурирует DTLS-handshake клиента.
type Dialer struct {
	// HandshakeTimeout ограничивает контекст handshake. Ноль - без таймаута.
	HandshakeTimeout time.Duration
	// HandshakeSem, если non-nil, ограничивает параллельные handshake
	// (Dial блокируется до появления слота или отмены ctx).
	HandshakeSem chan struct{}
}

// Dial захватывает опциональный handshake-слот и выполняет DTLS-handshake
// клиента поверх pc к peer. При успехе возвращает *dtls.Conn (закрывает вызывающий).
// Self-signed сертификат генерируется заново на каждый handshake: каждая
// TURN-сессия и каждый реконнект получают уникальный fingerprint, чтобы не
// коррелировать N параллельных стримов с одного IP как ботный трафик
// (DTLS здесь - для обфускации, не аутентификации; см. doc.go).
func (d *Dialer) Dial(ctx context.Context, pc net.PacketConn, peer *net.UDPAddr) (*dtls.Conn, error) {
	certificate, err := GenerateSelfSignedCert()
	if err != nil {
		return nil, err
	}
	if d.HandshakeSem != nil {
		select {
		case d.HandshakeSem <- struct{}{}:
			defer func() { <-d.HandshakeSem }()
		case <-ctx.Done():
			return nil, ctx.Err()
		}
	}

	hsCtx := ctx
	if d.HandshakeTimeout > 0 {
		var cancel context.CancelFunc
		hsCtx, cancel = context.WithTimeout(ctx, d.HandshakeTimeout)
		defer cancel()
	}

	dtlsConn, err := dtls.ClientWithOptions(
		pc,
		peer,
		dtls.WithCertificates(certificate),
		dtls.WithInsecureSkipVerify(true),
		dtls.WithExtendedMasterSecret(dtls.RequireExtendedMasterSecret),
		dtls.WithCipherSuites(dtls.TLS_ECDHE_ECDSA_WITH_AES_128_GCM_SHA256),
		dtls.WithConnectionIDGenerator(dtls.OnlySendCIDGenerator()),
	)
	if err != nil {
		return nil, err
	}
	if err := dtlsConn.HandshakeContext(hsCtx); err != nil {
		_ = dtlsConn.Close()
		return nil, err
	}
	return dtlsConn, nil
}
