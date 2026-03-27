package managed

import (
	"context"
	"fmt"
)

// rci provides helper methods for building and sending RCI POST payloads.
// All managed server operations use RCI instead of ndmc to avoid
// spamming router logs with session connect/disconnect messages.

// rciPost sends a JSON payload to RCI and returns an error if the call fails.
func (s *Service) rciPost(ctx context.Context, payload interface{}) error {
	_, err := s.ndms.RCIPost(ctx, payload)
	return err
}

// rciSave saves the NDMS configuration via RCI.
func (s *Service) rciSave(ctx context.Context) error {
	return s.rciPost(ctx, map[string]interface{}{
		"system": map[string]interface{}{
			"configuration": map[string]interface{}{
				"save": true,
			},
		},
	})
}

// rciCreateInterface creates a new WireGuard interface via RCI.
func (s *Service) rciCreateInterface(ctx context.Context, name string) error {
	return s.rciPost(ctx, map[string]interface{}{
		"interface": map[string]interface{}{
			name: map[string]interface{}{},
		},
	})
}

// rciDeleteInterface removes a WireGuard interface via RCI.
func (s *Service) rciDeleteInterface(ctx context.Context, name string) error {
	return s.rciPost(ctx, map[string]interface{}{
		"interface": map[string]interface{}{
			name: map[string]interface{}{
				"no": true,
			},
		},
	})
}

// rciConfigureServer sets all server interface properties in a single RCI call.
func (s *Service) rciConfigureServer(ctx context.Context, name, description, address, mask string, port int) error {
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

// rciSetListenPort updates the listen port.
func (s *Service) rciSetListenPort(ctx context.Context, ifaceName string, port int) error {
	return s.rciPost(ctx, map[string]interface{}{
		"interface": map[string]interface{}{
			ifaceName: map[string]interface{}{
				"wireguard": map[string]interface{}{
					"listen-port": map[string]interface{}{
						"port": port,
					},
				},
			},
		},
	})
}

// rciRemoveAddress removes an IP address from the interface.
func (s *Service) rciRemoveAddress(ctx context.Context, ifaceName, address, mask string) error {
	return s.rciPost(ctx, map[string]interface{}{
		"interface": map[string]interface{}{
			ifaceName: map[string]interface{}{
				"ip": map[string]interface{}{
					"address": map[string]interface{}{
						"no":      true,
						"address": address,
						"mask":    mask,
					},
				},
			},
		},
	})
}

// rciSetAddress sets an IP address on the interface.
func (s *Service) rciSetAddress(ctx context.Context, ifaceName, address, mask string) error {
	return s.rciPost(ctx, map[string]interface{}{
		"interface": map[string]interface{}{
			ifaceName: map[string]interface{}{
				"ip": map[string]interface{}{
					"address": map[string]interface{}{
						"address": address,
						"mask":    mask,
					},
				},
			},
		},
	})
}

// rciSetNAT enables or disables NAT for an interface.
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
func (s *Service) rciAddPeer(ctx context.Context, ifaceName, pubKey, psk, comment, peerIP string) error {
	peer := map[string]interface{}{
		"key":           pubKey,
		"preshared-key": psk,
		"connect":       true,
		"allow-ips": []map[string]interface{}{
			{"address": peerIP, "mask": "255.255.255.255"},
			{"address": "0.0.0.0", "mask": "0.0.0.0"},
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

// rciRemovePeer removes a peer by public key.
func (s *Service) rciRemovePeer(ctx context.Context, ifaceName, pubKey string) error {
	return s.rciPost(ctx, map[string]interface{}{
		"interface": map[string]interface{}{
			ifaceName: map[string]interface{}{
				"wireguard": map[string]interface{}{
					"peer": []map[string]interface{}{
						{"no": true, "key": pubKey},
					},
				},
			},
		},
	})
}

// rciSetPeerConnect enables or disables a peer.
func (s *Service) rciSetPeerConnect(ctx context.Context, ifaceName, pubKey string, connect bool) error {
	return s.rciPost(ctx, map[string]interface{}{
		"interface": map[string]interface{}{
			ifaceName: map[string]interface{}{
				"wireguard": map[string]interface{}{
					"peer": []map[string]interface{}{
						{"key": pubKey, "connect": connect},
					},
				},
			},
		},
	})
}

// rciSetPeerComment sets the description/comment for a peer.
func (s *Service) rciSetPeerComment(ctx context.Context, ifaceName, pubKey, comment string) error {
	return s.rciPost(ctx, map[string]interface{}{
		"interface": map[string]interface{}{
			ifaceName: map[string]interface{}{
				"wireguard": map[string]interface{}{
					"peer": []map[string]interface{}{
						{"key": pubKey, "comment": comment},
					},
				},
			},
		},
	})
}

// rciUpdatePeerAllowIPs removes old allow-ips and sets new ones.
func (s *Service) rciUpdatePeerAllowIPs(ctx context.Context, ifaceName, pubKey, oldIP, newIP string) error {
	// Remove old
	if oldIP != "" {
		if err := s.rciPost(ctx, map[string]interface{}{
			"interface": map[string]interface{}{
				ifaceName: map[string]interface{}{
					"wireguard": map[string]interface{}{
						"peer": []map[string]interface{}{
							{
								"key": pubKey,
								"allow-ips": []map[string]interface{}{
									{"no": true, "address": oldIP, "mask": "255.255.255.255"},
									{"no": true, "address": "0.0.0.0", "mask": "0.0.0.0"},
								},
							},
						},
					},
				},
			},
		}); err != nil {
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
								{"address": "0.0.0.0", "mask": "0.0.0.0"},
							},
						},
					},
				},
			},
		},
	})
}
