package managed

import (
	"context"
	"encoding/json"
	"fmt"

	"github.com/hoaxisr/awg-manager/internal/ndms/command"
)

// rci provides helper methods for building and sending RCI POST payloads.
// All managed server operations use RCI instead of ndmc to avoid
// spamming router logs with session connect/disconnect messages.

// rciPost sends a JSON payload to RCI and returns an error on a transport
// failure or on any nested status:"error" in the reply (NDMS answers HTTP 200
// to a rejected command). Like the command package, it schedules a debounced
// NDMS config save and invalidates caches an interface/wireguard-peer mutation
// could affect on BOTH paths: NDMS applies a payload element-wise, so a
// rejected reply may still carry applied changes.
func (s *Service) rciPost(ctx context.Context, payload interface{}) error {
	return s.rciPostTolerant(ctx, payload, nil)
}

// rciPostTolerant — rciPost, который признаёт отказы tolerate безобидными
// (предикаты command.Tolerate*): идемпотентный снос того, чего уже нет.
func (s *Service) rciPostTolerant(ctx context.Context, payload interface{}, tolerate func(string) bool) error {
	var after []func()
	if s.saveCoord != nil {
		after = append(after, s.saveCoord.Request)
	}
	if s.queries != nil {
		if s.queries.WGServers != nil {
			after = append(after, s.queries.WGServers.InvalidateAll)
		}
		if s.queries.Interfaces != nil {
			after = append(after, s.queries.Interfaces.InvalidateAll)
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

// rciCreateInterface creates a new WireGuard interface via RCI.
func (s *Service) rciCreateInterface(ctx context.Context, name string) error {
	return s.rciPost(ctx, map[string]interface{}{
		"interface": map[string]interface{}{
			name: map[string]interface{}{},
		},
	})
}

// rciDeleteInterface removes a WireGuard interface via RCI. Интерфейса уже нет
// (снесён мимо панели) — цель достигнута: иначе запись сервера не удалить.
func (s *Service) rciDeleteInterface(ctx context.Context, name string) error {
	return s.rciPostTolerant(ctx, map[string]interface{}{
		"interface": map[string]interface{}{
			name: map[string]interface{}{
				"no": true,
			},
		},
	}, command.TolerateMissingInterface)
}

// rciConfigureServer sets all server interface properties in a single RCI call.
func (s *Service) rciConfigureServer(ctx context.Context, name, description, address, mask string, port, mtu int) error {
	return s.rciPost(ctx, map[string]interface{}{
		"interface": map[string]interface{}{
			name: map[string]interface{}{
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
	})
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
func (s *Service) rciUpdateServer(ctx context.Context, ifaceName string, c updateServerChanges) error {
	iface := map[string]interface{}{}
	if c.descriptionSet {
		iface["description"] = c.description
	}
	if c.portSet {
		iface["wireguard"] = map[string]interface{}{
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
		iface["ip"] = ip
	}
	if len(iface) == 0 {
		return nil
	}
	return s.rciPost(ctx, map[string]interface{}{
		"interface": map[string]interface{}{
			ifaceName: iface,
		},
	})
}

// rciSetNAT enables or disables NAT for an interface. `no ip nat` на
// интерфейсе без NAT — успех («a NAT rule removed», стенд 5.02.A.11), допуск
// снятию не нужен.
func (s *Service) rciSetNAT(ctx context.Context, ifaceName string, enabled bool) error {
	if enabled {
		return s.rciPost(ctx, map[string]interface{}{
			"ip": map[string]interface{}{
				"nat": map[string]interface{}{
					"interface": ifaceName,
				},
			},
		})
	}
	return s.rciPost(ctx, map[string]interface{}{
		"ip": map[string]interface{}{
			"nat": []map[string]interface{}{
				{"no": true, "interface": ifaceName},
			},
		},
	})
}

// rciSetStaticNAT добавляет/снимает Static NAT (`ip static <iface> <wan>`)
// для интерфейса. wanIface — NDMS-имя WAN (to-interface, трекает динамический адрес).
func (s *Service) rciSetStaticNAT(ctx context.Context, ifaceName, wanIface string, enabled bool) error {
	if enabled {
		return s.rciPost(ctx, map[string]interface{}{
			"ip": map[string]interface{}{
				"static": map[string]interface{}{
					"interface":    ifaceName,
					"to-interface": wanIface,
				},
			},
		})
	}
	// Снятие терпит «unknown interface», как command.RemoveStaticNAT: выход
	// мог исчезнуть раньше правила.
	return s.rciPostTolerant(ctx, map[string]interface{}{
		"ip": map[string]interface{}{
			"static": []map[string]interface{}{
				{"no": true, "interface": ifaceName, "to-interface": wanIface},
			},
		},
	}, command.TolerateUnknownInterface)
}

// rciSetPrivateKey installs an explicit WireGuard private key on the
// interface via RCI. Verified against NDMS 4.x: POST returns "set private
// key." status and the next /show/interface/<name>.wireguard.public-key
// reflects the public key derived from the supplied private key. Used
// during Restore to install the backup's keypair so previously-distributed
// client .conf files keep working.
func (s *Service) rciSetPrivateKey(ctx context.Context, ifaceName, privateKey string) error {
	return s.rciPost(ctx, map[string]interface{}{
		"interface": map[string]interface{}{
			ifaceName: map[string]interface{}{
				"wireguard": map[string]interface{}{
					"private-key": privateKey,
				},
			},
		},
	})
}

// rciSetASCParams sets AWG ASC params on interface wireguard.asc.
// Caller must pass JSON object shape accepted by NDMS and already stripped
// from client-only signature fields (i1..i5).
func (s *Service) rciSetASCParams(ctx context.Context, ifaceName string, params json.RawMessage) error {
	var asc map[string]any
	if err := json.Unmarshal(params, &asc); err != nil {
		return fmt.Errorf("parse ASC params: %w", err)
	}
	return s.rciPost(ctx, map[string]interface{}{
		"interface": map[string]interface{}{
			ifaceName: map[string]interface{}{
				"wireguard": map[string]interface{}{
					"asc": asc,
				},
			},
		},
	})
}

// rciClearASCParams clears ASC settings from interface wireguard.asc.
func (s *Service) rciClearASCParams(ctx context.Context, ifaceName string) error {
	return s.rciPost(ctx, map[string]interface{}{
		"interface": map[string]interface{}{
			ifaceName: map[string]interface{}{
				"wireguard": map[string]interface{}{
					"asc": map[string]interface{}{
						"no": true,
					},
				},
			},
		},
	})
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
func (s *Service) rciSetHotspotPolicy(ctx context.Context, ifaceName, policy string) error {
	return s.rciPost(ctx, map[string]interface{}{
		"ip": map[string]interface{}{
			"hotspot": map[string]interface{}{
				"policy": map[string]interface{}{
					"interface": ifaceName,
					"policy":    policy,
				},
			},
		},
	})
}

// rciClearHotspotPolicy removes the ip hotspot policy from the interface
// (default-permit). Mirrors `no policy <iface>` in (config-hotspot) mode.
//
// Write form verified on a live router (router replies "interface ...
// policy cleared."):
//
//	{"ip":{"hotspot":{"policy":{"interface":"Wireguard0","no":true}}}}
func (s *Service) rciClearHotspotPolicy(ctx context.Context, ifaceName string) error {
	return s.rciPost(ctx, map[string]interface{}{
		"ip": map[string]interface{}{
			"hotspot": map[string]interface{}{
				"policy": map[string]interface{}{
					"interface": ifaceName,
					"no":        true,
				},
			},
		},
	})
}

// rciInterfaceUp brings the interface up.
func (s *Service) rciInterfaceUp(ctx context.Context, ifaceName string) error {
	return s.rciPost(ctx, map[string]interface{}{
		"interface": map[string]interface{}{
			ifaceName: map[string]interface{}{
				"up": true,
			},
		},
	})
}

// rciInterfaceDown brings the interface down.
func (s *Service) rciInterfaceDown(ctx context.Context, ifaceName string) error {
	return s.rciPost(ctx, map[string]interface{}{
		"interface": map[string]interface{}{
			ifaceName: map[string]interface{}{
				"up": false,
			},
		},
	})
}

// rciAddPeer adds a peer with all parameters in a single RCI call.
func (s *Service) rciAddPeer(ctx context.Context, ifaceName, pubKey, psk, comment, peerIP string, enabled bool) error {
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
			ifaceName: map[string]interface{}{
				"wireguard": map[string]interface{}{
					"peer": []map[string]interface{}{peer},
				},
			},
		},
	})
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
func (s *Service) rciRemovePeer(ctx context.Context, ifaceName, pubKey string) error {
	wg, err := s.peerCommands()
	if err != nil {
		return err
	}
	return wg.RemovePeer(ctx, ifaceName, pubKey)
}

// rciSetPeerConnect enables or disables a peer. comment must carry the peer's
// current name: a partial peer update without it makes NDMS wipe the stored
// comment (psk/allow-ips survive, comment does not). Пира на роутере нет —
// peersubnet.ErrPeerNotFound без поста.
func (s *Service) rciSetPeerConnect(ctx context.Context, ifaceName, pubKey string, connect bool, comment string) error {
	wg, err := s.peerCommands()
	if err != nil {
		return err
	}
	return wg.SetPeerConnect(ctx, ifaceName, pubKey, connect, comment)
}

// rciSetPeerComment sets the description/comment for a peer. Пира на роутере
// нет — peersubnet.ErrPeerNotFound без поста.
func (s *Service) rciSetPeerComment(ctx context.Context, ifaceName, pubKey, comment string) error {
	wg, err := s.peerCommands()
	if err != nil {
		return err
	}
	return wg.SetPeerComment(ctx, ifaceName, pubKey, comment)
}

// rciRemovePeerDefaultRoute strips the legacy 0.0.0.0/0 entry from a peer's
// allow-ips, leaving its /32 intact. Used by the one-time MigratePeerAllowIPs
// sweep over peers created by older builds. `no such net in peer` — 0.0.0.0/0
// у пира уже нет: цель миграции достигнута, а не отказ.
func (s *Service) rciRemovePeerDefaultRoute(ctx context.Context, ifaceName, pubKey string) error {
	return s.rciPostTolerant(ctx, map[string]interface{}{
		"interface": map[string]interface{}{
			ifaceName: map[string]interface{}{
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
	}, command.TolerateNoSuchNetInPeer)
}

// rciUpdatePeerAllowIPs removes old allow-ips and sets new ones. Снятие
// старого идёт командой с допуском `no such net in peer` (11.A/11.8): /32 уже
// может не стоять — после отказа отката или правки мимо панели, и такая смена
// иначе застревала бы навсегда.
func (s *Service) rciUpdatePeerAllowIPs(ctx context.Context, ifaceName, pubKey, oldIP, newIP string) error {
	if oldIP != "" {
		if s.commands == nil || s.commands.Wireguard == nil {
			return fmt.Errorf("ndms commands not wired")
		}
		if err := s.commands.Wireguard.RemovePeerAllowIP(ctx, ifaceName, pubKey, oldIP, "255.255.255.255"); err != nil {
			return fmt.Errorf("remove old allow-ips: %w", err)
		}
	}

	// Add new
	return s.rciPost(ctx, map[string]interface{}{
		"interface": map[string]interface{}{
			ifaceName: map[string]interface{}{
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
	})
}
