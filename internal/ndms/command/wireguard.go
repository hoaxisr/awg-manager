package command

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"strings"

	"github.com/hoaxisr/awg-manager/internal/ndms"
	"github.com/hoaxisr/awg-manager/internal/ndms/query"
	"github.com/hoaxisr/awg-manager/internal/peersubnet"
)

type WireguardCommands struct {
	ndmsMutator
}

// NewWireguardCommands паникует на nil s (newMutator).
func NewWireguardCommands(p Poster, s *SaveCoordinator, q *query.Queries) *WireguardCommands {
	return &WireguardCommands{ndmsMutator: newMutator(p, s, q)}
}

// SetASCParams sets the AmneziaWG ASC obfuscation parameters. The params
// json.RawMessage must be a JSON object with string values for
// jc/jmin/jmax/s1/s2 and hex strings for h1/h2/h3/h4 (OS ≥ 5.1 adds
// s3/s4/i1-i5). Caller is responsible for firmware-appropriate field set.
func (c *WireguardCommands) SetASCParams(ctx context.Context, iface query.Confirmed, params json.RawMessage) error {
	name := iface.Name()
	var asc map[string]any
	if err := json.Unmarshal(params, &asc); err != nil {
		return fmt.Errorf("set asc params %s: parse: %w", name, err)
	}
	payload := map[string]any{
		"interface": map[string]any{
			name: map[string]any{
				"wireguard": map[string]any{"asc": asc},
			},
		},
	}
	return postMutationChecked(ctx, c.poster, c.save, payload, "set asc params "+name,
		func() {
			c.queries.Interfaces.Invalidate(name)
			if c.queries.WGServers != nil {
				c.queries.WGServers.Invalidate(name)
			}
		},
		c.queries.RunningConfig.InvalidateAll)
}

// ResetASCParams снимает параметры ASC с интерфейса. Нужен, когда туннель
// уезжает с нативного пути на awg_proxy (конфиг 3.x на прошивке с ASC 2.0):
// оставленные в NDMS параметры прошивка продолжит применять, а kmod наложит
// свою обфускацию поверх — на выходе мусор.
//
// Форма ровно такая: структурный вид ({"asc":{"no":true}}) прошивка не
// принимает, а эта строка отвечает «reset ASC parameters» (проверено на
// 5.01.C.3.0-1).
func (c *WireguardCommands) ResetASCParams(ctx context.Context, iface query.Confirmed) error {
	name := iface.Name()
	payload := map[string]any{"parse": "interface " + name + " no wireguard asc"}
	return postMutationChecked(ctx, c.poster, c.save, payload, "reset asc params "+name,
		func() {
			c.queries.Interfaces.Invalidate(name)
			if c.queries.WGServers != nil {
				c.queries.WGServers.Invalidate(name)
			}
		},
		c.queries.RunningConfig.InvalidateAll)
}

// ImportResult holds the parsed outcome of a wireguard config import.
// Created — созданный интерфейс, подтверждённый свежим списком (F546).
// Intersects names a pre-existing interface the imported config collides
// with (empty if none); Messages are the human-readable status[] lines the
// router returned. Both are kept so the caller does not lose context even
// on a successful import.
type ImportResult struct {
	Created    query.Confirmed
	Intersects string
	Messages   []string
}

// ImportWireguardConfig uploads a .conf file to NDMS and returns the import
// result, including the created NDMS interface (e.g. "Wireguard1").
// confData is the raw .conf body (NOT base64 — encoded internally).
// Созданное подтверждается свежим списком: он же кладёт запись в кэш (F546).
func (c *WireguardCommands) ImportWireguardConfig(ctx context.Context, confData []byte, filename string) (ImportResult, error) {
	encoded := base64.StdEncoding.EncodeToString(confData)
	payload := map[string]any{
		"interface": map[string]any{
			"wireguard": map[string]any{
				"import":   encoded,
				"name":     "",
				"filename": filename,
			},
		},
	}
	resp, err := c.poster.Post(ctx, payload)
	if err != nil {
		return ImportResult{}, fmt.Errorf("import wireguard: %w", err)
	}

	// Real NDMS response shape (captured on 5.01.A.x):
	// {"interface":{"wireguard":{"import":{
	//   "intersects":"", "created":"Wireguard3",
	//   "status":[{"status":"message","code":"...","ident":"...","message":"..."}]}}}}
	var parsed struct {
		Interface struct {
			Wireguard struct {
				Import struct {
					Intersects string `json:"intersects"`
					Created    string `json:"created"`
					Status     []struct {
						Status  string `json:"status"`
						Message string `json:"message"`
					} `json:"status"`
				} `json:"import"`
			} `json:"wireguard"`
		} `json:"interface"`
	}
	if err := json.Unmarshal(resp, &parsed); err != nil {
		return ImportResult{}, fmt.Errorf("import wireguard: decode: %w", err)
	}
	imp := parsed.Interface.Wireguard.Import

	var msgs []string
	for _, s := range imp.Status {
		if s.Message != "" {
			msgs = append(msgs, s.Message)
		}
	}

	if imp.Created == "" {
		// The router accepted the request (HTTP 200) but did not return a
		// created interface. The reason lives in the nested status[] array,
		// which the top-level error envelope check does not inspect — surface
		// it instead of an opaque message.
		detail := strings.Join(msgs, "; ")
		if detail == "" {
			detail = "no status message"
		}
		return ImportResult{}, fmt.Errorf("import wireguard: router returned no created interface (intersects=%q; status: %s)", imp.Intersects, detail)
	}
	conf, err := c.confirmCreated(ctx, imp.Created, true)
	if err != nil {
		return ImportResult{}, fmt.Errorf("import wireguard: %w", err) // имя уже в ошибке подтверждения
	}
	return ImportResult{Created: conf, Intersects: imp.Intersects, Messages: msgs}, nil
}

func (c *WireguardCommands) invalidateServer(name string) {
	if c.queries == nil {
		return
	}
	if c.queries.Interfaces != nil {
		c.queries.Interfaces.Invalidate(name)
	}
	if c.queries.WGServers != nil {
		c.queries.WGServers.Invalidate(name)
	}
	if c.queries.RunningConfig != nil {
		c.queries.RunningConfig.InvalidateAll()
	}
}

// AddPeer adds a peer to a WireGuard server interface.
func (c *WireguardCommands) AddPeer(ctx context.Context, iface query.Confirmed, pubKey, psk, comment, peerIP string, enabled bool) error {
	ifaceName := iface.Name()
	peer := map[string]any{
		"key":           pubKey,
		"preshared-key": psk,
		"connect":       enabled,
		"allow-ips": []map[string]any{
			{"address": peerIP, "mask": "255.255.255.255"},
		},
	}
	if comment != "" {
		peer["comment"] = comment
	}
	payload := map[string]any{
		"interface": map[string]any{
			ifaceName: map[string]any{
				"wireguard": map[string]any{
					"peer": []map[string]any{peer},
				},
			},
		},
	}
	return postMutationChecked(ctx, c.poster, c.save, payload, "add peer "+ifaceName,
		func() { c.invalidateServer(ifaceName) })
}

// RemovePeer removes a peer by public key. Снятие отсутствующего пира NDMS
// отвергает вложенным `no input […]` (стенд 5.02.A.11) — фраза общая, по ней
// не терпим. Решает свежее чтение rc мимо кэша: пира с этим ключом нет —
// цель достигнута (пир удалён в веб-морде); есть — исходный отказ; чтение
// упало — отказ с ErrPeerPresenceUnknown. Один на оба пути (системный и managed).
func (c *WireguardCommands) RemovePeer(ctx context.Context, iface query.Confirmed, pubKey string) error {
	ifaceName := iface.Name()
	payload := map[string]any{
		"interface": map[string]any{
			ifaceName: map[string]any{
				"wireguard": map[string]any{
					"peer": []map[string]any{
						{"no": true, "key": pubKey},
					},
				},
			},
		},
	}
	err := postMutationChecked(ctx, c.poster, c.save, payload, "remove peer "+ifaceName,
		func() { c.invalidateServer(ifaceName) })
	if err == nil {
		return nil
	}
	present, rerr := c.PeerPresent(ctx, iface, pubKey)
	if rerr != nil {
		return fmt.Errorf("%w; %w: %v", err, ErrPeerPresenceUnknown, rerr)
	}
	if present {
		return err
	}
	return nil
}

// ErrPeerPresenceUnknown — снятие пира отказало, а перечитать список пиров не
// удалось: остался ли пир, неизвестно.
var ErrPeerPresenceUnknown = errors.New("peer presence unknown")

// freshPeerIn — пир с ключом на подтверждённом интерфейсе по свежему rc, мимо
// кэша; нет — peersubnet.ErrPeerNotFound. Список интерфейсов не перечитывается:
// подтверждение у вызывающего одно на весь поток (F546).
func (c *WireguardCommands) freshPeerIn(ctx context.Context, iface query.Confirmed, pubKey string) (*ndms.WireguardServerPeerConfig, error) {
	if c.queries == nil || c.queries.WGServers == nil {
		return nil, fmt.Errorf("wireguard server store not wired")
	}
	peers, err := c.queries.WGServers.PeersRC(ctx, iface)
	if err != nil {
		return nil, err
	}
	return findPeer(peers, iface.Name(), pubKey)
}

func findPeer(peers []ndms.WireguardServerPeerConfig, ifaceName, pubKey string) (*ndms.WireguardServerPeerConfig, error) {
	for i := range peers {
		if peers[i].PublicKey == pubKey {
			return &peers[i], nil
		}
	}
	return nil, fmt.Errorf("%s %s: %w", ifaceName, pubKey, peersubnet.ErrPeerNotFound)
}

// PeerPresent — есть ли пир с ключом на интерфейсе по свежему rc.
func (c *WireguardCommands) PeerPresent(ctx context.Context, iface query.Confirmed, pubKey string) (bool, error) {
	if _, err := c.freshPeerIn(ctx, iface, pubKey); err != nil {
		if errors.Is(err, peersubnet.ErrPeerNotFound) {
			return false, nil
		}
		return false, err
	}
	return true, nil
}

// requirePeer — перед операцией по ключу пира, кроме создания и снятия: любую
// такую операцию (allow-ips, comment, connect, preshared-key) на ключе,
// которого на интерфейсе нет, NDMS принимает, СОЗДАВАЯ пира (стенд 5.02.A.11,
// 28.09). Пира нет — peersubnet.ErrPeerNotFound, ни одного поста.
func (c *WireguardCommands) requirePeer(ctx context.Context, iface query.Confirmed, pubKey string) error {
	_, err := c.freshPeerIn(ctx, iface, pubKey)
	if err != nil && !errors.Is(err, peersubnet.ErrPeerNotFound) {
		return fmt.Errorf("check peer on router: %w", err)
	}
	return err
}

// SetPeerConnect enables or disables a peer. comment must carry the peer's
// current name: a partial peer update without it makes NDMS wipe the stored
// comment (psk/allow-ips survive, comment does not).
func (c *WireguardCommands) SetPeerConnect(ctx context.Context, iface query.Confirmed, pubKey string, connect bool, comment string) error {
	ifaceName := iface.Name()
	if err := c.requirePeer(ctx, iface, pubKey); err != nil {
		return err
	}
	peer := map[string]any{"key": pubKey, "connect": connect}
	if comment != "" {
		peer["comment"] = comment
	}
	payload := map[string]any{
		"interface": map[string]any{
			ifaceName: map[string]any{
				"wireguard": map[string]any{
					"peer": []map[string]any{peer},
				},
			},
		},
	}
	return postMutationChecked(ctx, c.poster, c.save, payload, "toggle peer "+ifaceName,
		func() { c.invalidateServer(ifaceName) })
}

// SetPeerComment sets the description/comment for a peer.
func (c *WireguardCommands) SetPeerComment(ctx context.Context, iface query.Confirmed, pubKey, comment string) error {
	ifaceName := iface.Name()
	if err := c.requirePeer(ctx, iface, pubKey); err != nil {
		return err
	}
	payload := map[string]any{
		"interface": map[string]any{
			ifaceName: map[string]any{
				"wireguard": map[string]any{
					"peer": []map[string]any{
						{"key": pubKey, "comment": comment},
					},
				},
			},
		},
	}
	return postMutationChecked(ctx, c.poster, c.save, payload, "rename peer "+ifaceName,
		func() { c.invalidateServer(ifaceName) })
}

// UpdatePeerAllowIPs removes old /32 and sets a new one. Снятие старого
// терпит `no such net in peer` (11.A/11.8): /32 уже может не стоять — после
// отказа отката или правки мимо панели, и смена адреса иначе застревала бы.
func (c *WireguardCommands) UpdatePeerAllowIPs(ctx context.Context, iface query.Confirmed, pubKey, oldIP, newIP string) error {
	if oldIP != "" {
		if err := c.RemovePeerAllowIP(ctx, iface, pubKey, oldIP, "255.255.255.255"); err != nil {
			return fmt.Errorf("remove old allow-ips: %w", err)
		}
	}
	return c.AddPeerAllowIP(ctx, iface, pubKey, newIP, "255.255.255.255")
}

// AddPeerAllowIP добавляет одну сеть в allow-ips пира. Повтор на уже стоящей
// сети NDMS принимает как успех — вызов идемпотентен (стенд 5.02.A.11, 27.09.2026).
func (c *WireguardCommands) AddPeerAllowIP(ctx context.Context, iface query.Confirmed, pubKey, address, mask string) error {
	ifaceName := iface.Name()
	return postMutationChecked(ctx, c.poster, c.save, peerAllowIPPayload(ifaceName, pubKey, address, mask, false),
		"add peer allow-ips "+ifaceName, func() { c.invalidateServer(ifaceName) })
}

// RemovePeerAllowIP снимает одну сеть. Отсутствующую NDMS отвергает фразой
// `no such net in peer` — для снятия и отката это цель, а не отказ.
func (c *WireguardCommands) RemovePeerAllowIP(ctx context.Context, iface query.Confirmed, pubKey, address, mask string) error {
	ifaceName := iface.Name()
	return postMutationCheckedTolerant(ctx, c.poster, c.save, peerAllowIPPayload(ifaceName, pubKey, address, mask, true),
		"remove peer allow-ips "+ifaceName, isNoSuchNetInPeer, func() { c.invalidateServer(ifaceName) })
}

func peerAllowIPPayload(ifaceName, pubKey, address, mask string, no bool) map[string]any {
	entry := map[string]any{"address": address, "mask": mask}
	if no {
		entry["no"] = true
	}
	return map[string]any{
		"interface": map[string]any{
			ifaceName: map[string]any{
				"wireguard": map[string]any{
					"peer": []map[string]any{{"key": pubKey, "allow-ips": []map[string]any{entry}}},
				},
			},
		},
	}
}
