package freeturn

// normalizeConfig upgrades v1 {client, server} on disk to v2 instance arrays.
// Returns true when the on-disk shape changed and should be rewritten.
func normalizeConfig(cfg *Config) bool {
	if cfg.Version >= ConfigVersion && len(cfg.Clients) > 0 && len(cfg.Servers) > 0 {
		changed := cfg.Client != (ClientConfig{}) || cfg.Server != (ServerConfig{}) || cfg.Version != ConfigVersion
		cfg.Version = ConfigVersion
		cfg.Client = ClientConfig{}
		cfg.Server = ServerConfig{}
		return changed
	}

	if len(cfg.Clients) == 0 {
		clientCfg := cfg.Client
		if clientCfg == (ClientConfig{}) {
			clientCfg = DefaultClientConfig()
		}
		name := "Клиент"
		if cfg.Client.Peer != "" {
			name = cfg.Client.Peer
		}
		cfg.Clients = []ClientInstance{{
			ID:     DefaultInstanceID,
			Name:   name,
			Config: clientCfg,
		}}
	}

	if len(cfg.Servers) == 0 {
		serverCfg := cfg.Server
		if serverCfg == (ServerConfig{}) {
			serverCfg = DefaultServerConfig()
		}
		name := "Сервер"
		if cfg.Server.Listen != "" {
			name = cfg.Server.Listen
		}
		cfg.Servers = []ServerInstance{{
			ID:     DefaultInstanceID,
			Name:   name,
			Config: serverCfg,
		}}
	}

	cfg.Version = ConfigVersion
	cfg.Client = ClientConfig{}
	cfg.Server = ServerConfig{}
	return true
}
