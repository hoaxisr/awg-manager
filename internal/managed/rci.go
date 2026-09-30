package managed

import (
	"context"
	"encoding/json"
	"fmt"

	"github.com/hoaxisr/awg-manager/internal/ndms/command"
	"github.com/hoaxisr/awg-manager/internal/ndms/query"
)

// rci provides helper methods for building and sending RCI POST payloads.
// All managed server operations use RCI instead of ndmc to avoid
// spamming router logs with session connect/disconnect messages.

// rciPost sends a JSON payload to RCI and returns an error on a transport
// failure or on any nested status:"error" in the reply (NDMS answers HTTP 200
// to a rejected command). Like the command package, it schedules a debounced
// NDMS config save and invalidates caches an interface/wireguard-peer mutation
// could affect on BOTH paths: NDMS applies a payload element-wise, so a
// rejected reply may still carry applied changes. Список интерфейсов не
// перечитывается: запись правят хуки NDMS, создание подтверждает
// rciCreateInterface, снос — Forget в rciDeleteInterface (F546).
//
// ifaces — подтверждения интерфейсов, по которым собран payload: нулевой
// Confirmed (Name()=="") — отказ без POST, иначе команда ушла бы по имени ""
// мимо подтверждения (F546).
func (s *Service) rciPost(ctx context.Context, payload interface{}, ifaces ...query.Confirmed) error {
	return s.rciPostTolerant(ctx, payload, nil, ifaces...)
}

// rciPostTolerant — rciPost, который признаёт отказы tolerate безобидными
// (предикаты command.Tolerate*): идемпотентный снос того, чего уже нет.
func (s *Service) rciPostTolerant(ctx context.Context, payload interface{}, tolerate func(string) bool, ifaces ...query.Confirmed) error {
	for _, c := range ifaces {
		if c.Name() == "" {
			return fmt.Errorf("managed rci: команда без подтверждённого интерфейса")
		}
	}
	var after []func()
	if s.saveCoord != nil {
		after = append(after, s.saveCoord.Request)
	}
	if s.queries != nil {
		if s.queries.WGServers != nil {
			after = append(after, s.queries.WGServers.InvalidateAll)
		}
		if s.queries.RunningConfig != nil {
			after = append(after, s.queries.RunningConfig.InvalidateAll)
		}
		// Удаление интерфейса уносит его маршруты — кэш владения обязан это увидеть.
		if s.queries.StaticRoutes != nil {
			after = append(after, s.queries.StaticRoutes.InvalidateAll)
		}
	}
	if err := command.PostChecked(ctx, s.transport, payload, "managed rci", tolerate, after...); err != nil {
		s.sysLog().Warn("managed rci post failed", "error", err)
		return err
	}
	return nil
}

// rciCreateInterface creates a new WireGuard interface via RCI и подтверждает
// его свежим списком: дальше весь поток идёт по этому доказательству (F546).
// Список не прочитан или записи в нём нет после принятой команды — ошибка без
// сноса: снести по имени без доказательства нельзя; остаток — в журнал.
func (s *Service) rciCreateInterface(ctx context.Context, name string) (query.Confirmed, error) {
	if s.queries == nil || s.queries.Interfaces == nil {
		return query.Confirmed{}, fmt.Errorf("interface store not wired")
	}
	if err := s.rciPost(ctx, map[string]interface{}{
		"interface": map[string]interface{}{
			name: map[string]interface{}{},
		},
	}); err != nil {
		return query.Confirmed{}, err
	}
	c, _, ok, err := s.queries.Interfaces.Confirm(ctx, name)
	if err == nil && !ok {
		err = fmt.Errorf("interface %s: NDMS принял команду, но записи в списке нет", name)
	}
	if err != nil {
		s.appLog.Warn("create", name, "интерфейс создан, но не подтверждён списком и не снесён: "+err.Error())
		return query.Confirmed{}, err
	}
	return c, nil
}

// rciDeleteInterface removes a WireGuard interface via RCI. Интерфейса уже нет
// (снесён мимо панели между подтверждением и командой) — цель достигнута.
// Снято — запись забывается сразу, не дожидаясь ifdestroyed.
func (s *Service) rciDeleteInterface(ctx context.Context, iface query.Confirmed) error {
	if err := s.rciPostTolerant(ctx, map[string]interface{}{
		"interface": map[string]interface{}{
			iface.Name(): map[string]interface{}{
				"no": true,
			},
		},
	}, command.TolerateMissingInterface, iface); err != nil {
		return err
	}
	if s.queries != nil && s.queries.Interfaces != nil {
		s.queries.Interfaces.Forget(iface.Name())
	}
	return nil
}

// rciConfigureServer sets all server interface properties in a single RCI call.
func (s *Service) rciConfigureServer(ctx context.Context, iface query.Confirmed, description, address, mask string, port, mtu int) error {
	return s.rciPost(ctx, map[string]interface{}{
		"interface": map[string]interface{}{
			iface.Name(): map[string]interface{}{
				"description": description,
				"security-level": map[string]interface{}{
					"private": true,
				},
				"wireguard": map[string]interface{}{
					"listen-port": map[string]interface{}{
						"port": port,
					},
				},
				"ip": map[string]interface{}{
					"address": map[string]interface{}{
						"address": address,
						"mask":    mask,
					},
					"mtu":          mtu,
					"name-servers": true,
					"tcp": map[string]interface{}{
						"adjust-mss": map[string]interface{}{
							"pmtu": true,
						},
					},
				},
				"up": true,
			},
		},
	}, iface)
}

// updateServerChanges holds the optional set of mutations rciUpdateServer
// applies in a single atomic POST. Only fields with the corresponding flag
// set are emitted into the payload.
type updateServerChanges struct {
	descriptionSet bool
	description    string

	portSet bool
	port    int

	addressChanged      bool
	oldAddress, oldMask string
	newAddress, newMask string

	mtuSet bool
	mtu    int
}

// rciUpdateServer applies multiple managed-server property changes in a
// single RCI POST. NDMS treats the nested `interface.<name>.{...}` object
// as one config transaction — either every leaf applies or the whole
// payload is rejected, so partial-failure divergence cannot occur.
//
// For an address change, both the removal of the old address and the
// installation of the new one are sent as an `ip.address` array of two
// entries; this mirrors the array-of-ops shape NDMS already uses for
// peers, hotspot policy, and NAT.
func (s *Service) rciUpdateServer(ctx context.Context, iface query.Confirmed, c updateServerChanges) error {
	body := map[string]interface{}{}
	if c.descriptionSet {
		body["description"] = c.description
	}
	if c.portSet {
		body["wireguard"] = map[string]interface{}{
			"listen-port": map[string]interface{}{
				"port": c.port,
			},
		}
	}
	ip := map[string]interface{}{}
	if c.addressChanged {
		ip["address"] = []map[string]interface{}{
			{"no": true, "address": c.oldAddress, "mask": c.oldMask},
			{"address": c.newAddress, "mask": c.newMask},
		}
	}
	if c.mtuSet {
		ip["mtu"] = c.mtu
	}
	if len(ip) > 0 {
		body["ip"] = ip
	}
	if len(body) == 0 {
		return nil
	}
	return s.rciPost(ctx, map[string]interface{}{
		"interface": map[string]interface{}{
			iface.Name(): body,
		},
	}, iface)
}

// rciSetNAT enables or disables NAT for an interface. `no ip nat` на
// интерфейсе без NAT — успех («a NAT rule removed», стенд 5.02.A.11), допуск
// снятию не нужен.
func (s *Service) rciSetNAT(ctx context.Context, iface query.Confirmed, enabled bool) error {
	if enabled {
		return s.rciPost(ctx, map[string]interface{}{
			"ip": map[string]interface{}{
				"nat": map[string]interface{}{
					"interface": iface.Name(),
				},
			},
		}, iface)
	}
	return s.rciPost(ctx, map[string]interface{}{
		"ip": map[string]interface{}{
			"nat": []map[string]interface{}{
				{"no": true, "interface": iface.Name()},
			},
		},
	}, iface)
}

// rciSetStaticNAT добавляет/снимает Static NAT (`ip static <iface> <wan>`)
// для интерфейса. wan — WAN (to-interface, трекает динамический адрес); оба
// подтверждены: ссылка на отсутствующий из `ip static` — E в журнале ndm.
func (s *Service) rciSetStaticNAT(ctx context.Context, iface, wan query.Confirmed, enabled bool) error {
	if enabled {
		return s.rciPost(ctx, map[string]interface{}{
			"ip": map[string]interface{}{
				"static": map[string]interface{}{
					"interface":    iface.Name(),
					"to-interface": wan.Name(),
				},
			},
		}, iface, wan)
	}
	// Снятие терпит «unknown interface», как command.RemoveStaticNAT: выход
	// мог исчезнуть раньше правила.
	return s.rciPostTolerant(ctx, map[string]interface{}{
		"ip": map[string]interface{}{
			"static": []map[string]interface{}{
				{"no": true, "interface": iface.Name(), "to-interface": wan.Name()},
			},
		},
	}, command.TolerateUnknownInterface, iface, wan)
}

// rciSetPrivateKey installs an explicit WireGuard private key on the
// interface via RCI. Verified against NDMS 4.x: POST returns "set private
// key." status and the next /show/interface/<name>.wireguard.public-key
// reflects the public key derived from the supplied private key. Used
// during Restore to install the backup's keypair so previously-distributed
// client .conf files keep working.
func (s *Service) rciSetPrivateKey(ctx context.Context, iface query.Confirmed, privateKey string) error {
	return s.rciPost(ctx, map[string]interface{}{
		"interface": map[string]interface{}{
			iface.Name(): map[string]interface{}{
				"wireguard": map[string]interface{}{
					"private-key": privateKey,
				},
			},
		},
	}, iface)
}

// rciSetASCParams sets AWG ASC params on interface wireguard.asc.
// Caller must pass JSON object shape accepted by NDMS and already stripped
// from client-only signature fields (i1..i5).
func (s *Service) rciSetASCParams(ctx context.Context, iface query.Confirmed, params json.RawMessage) error {
	var asc map[string]any
	if err := json.Unmarshal(params, &asc); err != nil {
		return fmt.Errorf("parse ASC params: %w", err)
	}
	return s.rciPost(ctx, map[string]interface{}{
		"interface": map[string]interface{}{
			iface.Name(): map[string]interface{}{
				"wireguard": map[string]interface{}{
					"asc": asc,
				},
			},
		},
	}, iface)
}

// rciClearASCParams clears ASC settings from interface wireguard.asc.
func (s *Service) rciClearASCParams(ctx context.Context, iface query.Confirmed) error {
	return s.rciPost(ctx, map[string]interface{}{
		"interface": map[string]interface{}{
			iface.Name(): map[string]interface{}{
				"wireguard": map[string]interface{}{
					"asc": map[string]interface{}{
						"no": true,
					},
				},
			},
		},
	}, iface)
}

// rciSetHotspotPolicy applies an ip hotspot policy to the interface.
// policy is an IP Policy profile name. For "none" use
// rciClearHotspotPolicy.
//
// Write form verified on a live router. NB: the /show/rc/ip/hotspot read
// form is an array keyed by "access"; the write command is a single
// object keyed by "policy" (router replies "policy ... applied to
// interface ..."):
//
//	{"ip":{"hotspot":{"policy":{"interface":"Wireguard0","policy":"Policy0"}}}}
func (s *Service) rciSetHotspotPolicy(ctx context.Context, iface query.Confirmed, policy string) error {
	return s.rciPost(ctx, map[string]interface{}{
		"ip": map[string]interface{}{
			"hotspot": map[string]interface{}{
				"policy": map[string]interface{}{
					"interface": iface.Name(),
					"policy":    policy,
				},
			},
		},
	}, iface)
}

// rciClearHotspotPolicy removes the ip hotspot policy from the interface
// (default-permit). Mirrors `no policy <iface>` in (config-hotspot) mode.
//
// Write form verified on a live router (router replies "interface ...
// policy cleared."):
//
//	{"ip":{"hotspot":{"policy":{"interface":"Wireguard0","no":true}}}}
func (s *Service) rciClearHotspotPolicy(ctx context.Context, iface query.Confirmed) error {
	return s.rciPost(ctx, map[string]interface{}{
		"ip": map[string]interface{}{
			"hotspot": map[string]interface{}{
				"policy": map[string]interface{}{
					"interface": iface.Name(),
					"no":        true,
				},
			},
		},
	}, iface)
}

// rciInterfaceUp brings the interface up.
func (s *Service) rciInterfaceUp(ctx context.Context, iface query.Confirmed) error {
	return s.rciPost(ctx, map[string]interface{}{
		"interface": map[string]interface{}{
			iface.Name(): map[string]interface{}{
				"up": true,
			},
		},
	}, iface)
}

// rciInterfaceDown brings the interface down.
func (s *Service) rciInterfaceDown(ctx context.Context, iface query.Confirmed) error {
	return s.rciPost(ctx, map[string]interface{}{
		"interface": map[string]interface{}{
			iface.Name(): map[string]interface{}{
				"up": false,
			},
		},
	}, iface)
}

// rciAddPeer adds a peer with all parameters in a single RCI call.
func (s *Service) rciAddPeer(ctx context.Context, iface query.Confirmed, pubKey, psk, comment, peerIP string, enabled bool) error {
	peer := map[string]interface{}{
		"key":           pubKey,
		"preshared-key": psk,
		"connect":       enabled,
		"allow-ips": []map[string]interface{}{
			{"address": peerIP, "mask": "255.255.255.255"},
		},
	}
	if comment != "" {
		peer["comment"] = comment
	}
	return s.rciPost(ctx, map[string]interface{}{
		"interface": map[string]interface{}{
			iface.Name(): map[string]interface{}{
				"wireguard": map[string]interface{}{
					"peer": []map[string]interface{}{peer},
				},
			},
		},
	}, iface)
}

// peerCommands — команды пира, общие с системным путём: снятие пира
// («уже снят» — по свежему rc) и проверка наличия перед правкой по ключу
// живут в одном месте, command.WireguardCommands.
func (s *Service) peerCommands() (*command.WireguardCommands, error) {
	if s.commands == nil || s.commands.Wireguard == nil {
		return nil, fmt.Errorf("ndms commands not wired")
	}
	return s.commands.Wireguard, nil
}

// rciRemovePeer removes a peer by public key; пир, уже снятый мимо панели, —
// успех (см. command.WireguardCommands.RemovePeer).
func (s *Service) rciRemovePeer(ctx context.Context, iface query.Confirmed, pubKey string) error {
	wg, err := s.peerCommands()
	if err != nil {
		return err
	}
	return wg.RemovePeer(ctx, iface, pubKey)
}

// rciSetPeerConnect enables or disables a peer. comment must carry the peer's
// current name: a partial peer update without it makes NDMS wipe the stored
// comment (psk/allow-ips survive, comment does not). Пира на роутере нет —
// peersubnet.ErrPeerNotFound без поста.
func (s *Service) rciSetPeerConnect(ctx context.Context, iface query.Confirmed, pubKey string, connect bool, comment string) error {
	wg, err := s.peerCommands()
	if err != nil {
		return err
	}
	return wg.SetPeerConnect(ctx, iface, pubKey, connect, comment)
}

// rciSetPeerComment sets the description/comment for a peer. Пира на роутере
// нет — peersubnet.ErrPeerNotFound без поста.
func (s *Service) rciSetPeerComment(ctx context.Context, iface query.Confirmed, pubKey, comment string) error {
	wg, err := s.peerCommands()
	if err != nil {
		return err
	}
	return wg.SetPeerComment(ctx, iface, pubKey, comment)
}

// rciRemovePeerDefaultRoute strips the legacy 0.0.0.0/0 entry from a peer's
// allow-ips, leaving its /32 intact. Used by the one-time MigratePeerAllowIPs
// sweep over peers created by older builds. `no such net in peer` — 0.0.0.0/0
// у пира уже нет: цель миграции достигнута, а не отказ.
func (s *Service) rciRemovePeerDefaultRoute(ctx context.Context, iface query.Confirmed, pubKey string) error {
	return s.rciPostTolerant(ctx, map[string]interface{}{
		"interface": map[string]interface{}{
			iface.Name(): map[string]interface{}{
				"wireguard": map[string]interface{}{
					"peer": []map[string]interface{}{
						{
							"key": pubKey,
							"allow-ips": []map[string]interface{}{
								{"no": true, "address": "0.0.0.0", "mask": "0.0.0.0"},
							},
						},
					},
				},
			},
		},
	}, command.TolerateNoSuchNetInPeer, iface)
}

// rciUpdatePeerAllowIPs removes old allow-ips and sets new ones. Снятие
// старого идёт командой с допуском `no such net in peer` (11.A/11.8): /32 уже
// может не стоять — после отказа отката или правки мимо панели, и такая смена
// иначе застревала бы навсегда.
func (s *Service) rciUpdatePeerAllowIPs(ctx context.Context, iface query.Confirmed, pubKey, oldIP, newIP string) error {
	if oldIP != "" {
		if s.commands == nil || s.commands.Wireguard == nil {
			return fmt.Errorf("ndms commands not wired")
		}
		if err := s.commands.Wireguard.RemovePeerAllowIP(ctx, iface, pubKey, oldIP, "255.255.255.255"); err != nil {
			return fmt.Errorf("remove old allow-ips: %w", err)
		}
	}

	// Add new
	return s.rciPost(ctx, map[string]interface{}{
		"interface": map[string]interface{}{
			iface.Name(): map[string]interface{}{
				"wireguard": map[string]interface{}{
					"peer": []map[string]interface{}{
						{
							"key": pubKey,
							"allow-ips": []map[string]interface{}{
								{"address": newIP, "mask": "255.255.255.255"},
							},
						},
					},
				},
			},
		},
	}, iface)
}
