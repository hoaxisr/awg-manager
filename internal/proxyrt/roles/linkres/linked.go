package linkres

import (
	"context"
	"fmt"
	"strconv"
	"time"

	"github.com/hoaxisr/awg-manager/internal/proxyrt"
)

// LinkedTunnel — связанный AWG-туннель (связь: WdttClientID /
// FreeTurnClientID в записи туннеля).
type LinkedTunnel struct {
	ID       string
	Endpoint string
}

// EndpointSync — контракт с туннельной подсистемой (закрывает открытый вопрос
// §13 спеки). Туннельная подсистема владеет записью; прокси — единственный
// писатель РОВНО одного поля связанных записей: Peer.Endpoint, и пишет туда
// ровно 127.0.0.1:<порт listen>. Sync применяет изменение и на живом
// интерфейсе, и в хранилище (методы хендлеров SyncLinkedTunnelEndpoints
// поверх хелпера linked_tunnels_lifecycle.go:190). Ручная правка endpoint'а —
// дрейф, который чинится: endpoint связанного туннеля по построению локальный.
type EndpointSync interface {
	List(ctx context.Context, clientID string) ([]LinkedTunnel, error)
	Sync(ctx context.Context, clientID, listen string) (int, error)
}

// LinkedEndpoint — ресурс linked_endpoint.
type LinkedEndpoint struct {
	id       proxyrt.ResourceID
	sync     EndpointSync
	clientID string
	listen   string
}

func NewLinkedEndpoint(id proxyrt.ResourceID, sync EndpointSync) *LinkedEndpoint {
	return &LinkedEndpoint{id: id, sync: sync}
}

func (l *LinkedEndpoint) SetDesired(clientID, listen string) {
	l.clientID, l.listen = clientID, listen
}

func (l *LinkedEndpoint) ID() proxyrt.ResourceID { return l.id }

func (l *LinkedEndpoint) Observe(ctx context.Context) (proxyrt.Observation, error) {
	tunnels, err := l.sync.List(ctx, l.clientID)
	if err != nil {
		return proxyrt.Observation{}, err
	}
	want, werr := localPort(l.listen)
	if werr != nil {
		return proxyrt.Observation{}, werr
	}
	drift := 0
	for _, t := range tunnels {
		if t.Endpoint != fmt.Sprintf("127.0.0.1:%d", want) {
			drift++
		}
	}
	return proxyrt.Observation{Known: true, Exists: drift == 0,
		Attrs: map[string]string{"drift": strconv.Itoa(drift), "total": strconv.Itoa(len(tunnels))}}, nil
}

func (l *LinkedEndpoint) Plan(obs proxyrt.Observation) []proxyrt.Step {
	if obs.Attrs["drift"] == "0" {
		return nil
	}
	return []proxyrt.Step{{Resource: l.id, Op: "sync",
		Args: map[string]string{"listen": l.listen}, Reason: "endpoint связанных туннелей отстал от listen"}}
}

func (l *LinkedEndpoint) Apply(ctx context.Context, s proxyrt.Step) error {
	if s.Op != "sync" {
		return fmt.Errorf("неизвестный шаг %q", s.Op)
	}
	_, err := l.sync.Sync(ctx, l.clientID, l.listen)
	return err
}

func (l *LinkedEndpoint) RecheckAfter() time.Duration { return 0 }
