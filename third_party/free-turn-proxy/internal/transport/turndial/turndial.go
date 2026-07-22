// Package turndial централизует TURN dial+allocate pipeline, общий для
// UDP (oneTurnConnection) и VLESS (createSmuxSession) режимов клиента.
//
// Один вызов Open выполняет: парсинг цели, применение host/port override,
// резолв UDP-адреса, dial UDP-или-TCP (с SplitFirstWriteConn поверх TCP),
// turn.NewClient, Listen, Allocate. Возвращает relay PacketConn и Close,
// который разрушает аллокацию, TURN-клиент и транспорт.
package turndial

import (
	"context"
	"fmt"
	"net"
	"sync"
	"time"

	"github.com/pion/logging"
	"github.com/pion/turn/v5"
	"github.com/samosvalishe/free-turn-proxy/internal/netconn"
	"github.com/samosvalishe/free-turn-proxy/internal/netctl"
	"github.com/samosvalishe/free-turn-proxy/internal/randx"
)

// Config конфигурирует один вызов Open.
type Config struct {
	// HostOverride, если непустой, заменяет host из lookup credentials.
	HostOverride string
	// PortOverride, если непустой, заменяет port из lookup credentials.
	PortOverride string
	// TransportUDP=true - dial TURN по UDP; иначе по TCP через STUNConn.
	TransportUDP bool
	// DialTimeout ограничивает TCP dial. Ноль -> 5s.
	DialTimeout time.Duration
}

// Stream - активная TURN-аллокация с зависимостями. Close разрушает в обратном порядке.
type Stream struct {
	// Relay - выделенный relay PacketConn из turn.Client.Allocate.
	Relay net.PacketConn
	// ServerUDPAddr - резолвнутый UDP-адрес TURN-сервера (host:port).
	ServerUDPAddr *net.UDPAddr
	// PermDead закрывается при стойком провале ChannelBind refresh (relay
	// блэкхолит data-path) - вызывающий рециклит allocation. См. permwatch.go.
	PermDead <-chan struct{}
	close    func() error
}

// Close освобождает аллокацию, TURN-клиент и транспорт.
// Безопасно вызвать один раз. Возвращает первую non-nil ошибку.
func (s *Stream) Close() error {
	if s == nil || s.close == nil {
		return nil
	}
	return s.close()
}

// Open подключается к TURN, создаёт turn.Client и выделяет relay. rawAddr -
// host:port из lookup credentials; user/pass - долгосрочные TURN-реквизиты.
func Open(ctx context.Context, cfg Config, peer *net.UDPAddr, user, pass, rawAddr string) (*Stream, error) {
	urlhost, urlport, err := net.SplitHostPort(rawAddr)
	if err != nil {
		return nil, fmt.Errorf("parse TURN addr: %w", err)
	}
	if cfg.HostOverride != "" {
		urlhost = cfg.HostOverride
	}
	if cfg.PortOverride != "" {
		urlport = cfg.PortOverride
	}
	turnServerAddr := net.JoinHostPort(urlhost, urlport)
	turnServerUDPAddr, err := net.ResolveUDPAddr("udp", turnServerAddr)
	if err != nil {
		return nil, fmt.Errorf("resolve TURN addr: %w", err)
	}
	turnServerAddr = turnServerUDPAddr.String()

	dialTimeout := cfg.DialTimeout
	if dialTimeout == 0 {
		dialTimeout = 5 * time.Second
	}

	var (
		turnConn  net.PacketConn
		closeConn func() error
	)
	if cfg.TransportUDP {
		raw, derr := (&net.Dialer{Control: netctl.Apply}).Dial("udp", turnServerUDPAddr.String())
		if derr != nil {
			return nil, fmt.Errorf("dial TURN (udp): %w", derr)
		}
		c, ok := raw.(*net.UDPConn)
		if !ok {
			_ = raw.Close()
			return nil, fmt.Errorf("turndial: expected *net.UDPConn, got %T", raw)
		}
		turnConn = &netconn.ConnectedUDPConn{UDPConn: c}
		closeConn = c.Close
	} else {
		dctx, cancel := context.WithTimeout(ctx, dialTimeout)
		defer cancel()
		var d net.Dialer
		c, derr := d.DialContext(dctx, "tcp", turnServerAddr)
		if derr != nil {
			return nil, fmt.Errorf("dial TURN (tcp): %w", derr)
		}
		// Рез внутри STUN magic cookie (байты 4-7) рвёт DPI-матч на cookie.
		// Offset рандомен в [5,7] - убирает статический фингерпринт фикс-offset.
		wrapped := &netconn.SplitFirstWriteConn{Conn: c, SplitAt: 5 + randx.Intn(3), Delay: 20 * time.Millisecond}
		turnConn = turn.NewSTUNConn(wrapped)
		closeConn = c.Close
	}

	var addrFamily turn.RequestedAddressFamily
	if peer.IP.To4() != nil {
		addrFamily = turn.RequestedAddressFamilyIPv4
	} else {
		addrFamily = turn.RequestedAddressFamilyIPv6
	}

	// Standalone CreatePermission refresh VK реджектит 400 - глушим (24h).
	// Permission держится живым через ChannelBind refresh (RFC 8656 §11);
	// блэкхол ловим по провалу этого байнда (см. permwatch.go).
	permDead := make(chan struct{})
	var permOnce sync.Once
	loggerFactory := &permWatchFactory{
		inner:     logging.NewDefaultLoggerFactory(),
		threshold: permFailThreshold,
		onDead:    func() { permOnce.Do(func() { close(permDead) }) },
	}
	client, err := turn.NewClient(&turn.ClientConfig{
		STUNServerAddr:            turnServerAddr,
		TURNServerAddr:            turnServerAddr,
		Conn:                      turnConn,
		Net:                       netconn.New(),
		Username:                  user,
		Password:                  pass,
		RequestedAddressFamily:    addrFamily,
		PermissionRefreshInterval: 24 * time.Hour,
		LoggerFactory:             loggerFactory,
	})
	if err != nil {
		if cerr := closeConn(); cerr != nil {
			err = fmt.Errorf("%w (close: %v)", err, cerr)
		}
		return nil, fmt.Errorf("create TURN client: %w", err)
	}
	if err = client.Listen(); err != nil {
		client.Close()
		if cerr := closeConn(); cerr != nil {
			err = fmt.Errorf("%w (close: %v)", err, cerr)
		}
		return nil, fmt.Errorf("TURN listen: %w", err)
	}
	relay, err := client.Allocate()
	if err != nil {
		client.Close()
		if cerr := closeConn(); cerr != nil {
			err = fmt.Errorf("%w (close: %v)", err, cerr)
		}
		return nil, fmt.Errorf("TURN allocate: %w", err)
	}

	return &Stream{
		Relay:         relay,
		ServerUDPAddr: turnServerUDPAddr,
		PermDead:      permDead,
		close: func() error {
			var firstErr error
			if cerr := relay.Close(); cerr != nil {
				firstErr = cerr
			}
			client.Close()
			if cerr := closeConn(); cerr != nil && firstErr == nil {
				firstErr = cerr
			}
			return firstErr
		},
	}, nil
}
