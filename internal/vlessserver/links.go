package vlessserver

import (
	"encoding/json"
	"errors"
	"fmt"
	"net/url"
	"strings"
)

func (s *Service) GenerateLinks(clientID string) (*ShareLinks, error) {
	return s.GenerateLinksForEndpoint(clientID, "", "")
}

func (s *Service) GenerateLinksForDomain(clientID string, domainOverride string) (*ShareLinks, error) {
	return s.GenerateLinksForEndpoint(clientID, "", domainOverride)
}

func (s *Service) GenerateLinksForEndpoint(clientID string, endpointID string, domainOverride string) (*ShareLinks, error) {
	s.mu.Lock()
	defer s.mu.Unlock()

	if s.recoveryRequired {
		return nil, ErrRecoveryRequired
	}

	idx := -1
	for i := range s.config.Clients {
		if s.config.Clients[i].ID == clientID {
			idx = i
			break
		}
	}
	if idx < 0 {
		return nil, errors.New("client not found")
	}

	if !hasValidFPCredentials(s.config.Clients[idx]) {
		candidate := s.cloneConfigLocked()
		if _, err := ensureClientFPCredentials(&candidate.Clients[idx]); err != nil {
			return nil, err
		}

		txID, err := s.prepareCandidateLocked("", candidate)
		if err != nil {
			return nil, fmt.Errorf("prepare fp credentials candidate: %w", err)
		}
		if err := s.commitPreparedLocked(txID); err != nil {
			return nil, fmt.Errorf("commit fp credentials candidate: %w", err)
		}
		if err := s.finalizePreparedLocked(txID); err != nil {
			return nil, fmt.Errorf("finalize fp credentials candidate: %w", err)
		}

		if s.recoveryRequired {
			return nil, ErrRecoveryRequired
		}
		idx = -1
		for i := range s.config.Clients {
			if s.config.Clients[i].ID == clientID {
				idx = i
				break
			}
		}
		if idx < 0 {
			return nil, errors.New("client not found")
		}
		if !hasValidFPCredentials(s.config.Clients[idx]) {
			return nil, errors.New("failed to persist valid FiniteProto v2 credentials")
		}
	}

	client := &s.config.Clients[idx]
	cfg := s.config
	cl := *client
	eps := SynthesizeEndpoints(s.config)

	var matchedEp *EndpointConfig
	if endpointID != "" {
		for i := range eps {
			if eps[i].ID == endpointID {
				matchedEp = &eps[i]
				break
			}
		}
	}
	if matchedEp == nil && domainOverride != "" {
		domLower := strings.ToLower(strings.TrimSpace(domainOverride))
		for i := range eps {
			if strings.EqualFold(eps[i].ID, domainOverride) || strings.EqualFold(eps[i].PublicDomain, domainOverride) {
				matchedEp = &eps[i]
				break
			}
			for _, part := range strings.Split(eps[i].PublicDomain, ",") {
				if strings.ToLower(strings.TrimSpace(part)) == domLower {
					matchedEp = &eps[i]
					break
				}
			}
			if matchedEp != nil {
				break
			}
		}
	}
	if matchedEp != nil {
		cfg.PublicDomain = matchedEp.PublicDomain
		if domainOverride != "" && !strings.EqualFold(matchedEp.ID, domainOverride) {
			cfg.PublicDomain = domainOverride
		}
		if matchedEp.PublicPort > 0 {
			cfg.PublicPort = matchedEp.PublicPort
		}
		if matchedEp.Path != "" {
			cfg.Path = matchedEp.Path
		}
		if matchedEp.Transport != "" {
			cfg.Transport = matchedEp.Transport
		}
		if matchedEp.Mode != "" {
			cfg.Mode = matchedEp.Mode
		}
		if matchedEp.UplinkMethod != "" {
			cfg.UplinkMethod = matchedEp.UplinkMethod
		}
		if matchedEp.XHTTPTuning != nil {
			t := *matchedEp.XHTTPTuning
			cfg.XHTTPTuning = &t
		}
		if (endpointID != "" || domainOverride != "") && len(eps) > 1 && matchedEp.Name != "" {
			if !strings.Contains(strings.ToLower(cl.Remark), strings.ToLower(matchedEp.Name)) {
				cl.Remark = strings.TrimSpace(cl.Remark) + " · " + matchedEp.Name
			}
		}
	} else if domainOverride != "" {
		cfg.PublicDomain = domainOverride
	}
	return BuildShareLinks(cfg, cl)
}

// BuildShareLinks generates client share links from configuration and client metadata.
// It is a pure function that does not depend on service runtime state.
func BuildShareLinks(cfg Config, client Client) (*ShareLinks, error) {
	if err := validateSupportedTransport(cfg.Transport); err != nil {
		return nil, err
	}
	if client.ID == "" {
		return nil, errors.New("client UUID is required")
	}
	domain := strings.TrimSpace(cfg.PublicDomain)
	if strings.Contains(domain, ",") {
		domain = strings.TrimSpace(strings.Split(domain, ",")[0])
	}
	if domain == "" {
		return nil, errors.New("public domain is required")
	}
	port := cfg.PublicPort
	if port <= 0 {
		port = 443
	}
	path := cfg.Path
	if path == "" {
		path = "/cdn-bridge"
	}
	if !strings.HasPrefix(path, "/") {
		path = "/" + path
	}
	mode := cfg.Mode
	if mode == "" {
		mode = "packet-up"
	}
	isFiniteProto := strings.EqualFold(mode, "finiteproto") || strings.EqualFold(cfg.Transport, "finiteproto")
	xhttpMode := mode
	if isFiniteProto {
		xhttpMode = "packet-up"
	}
	method := cfg.UplinkMethod
	if method == "" {
		method = "GET"
	}

	effectivePath := path
	var xhttpExtra []byte
	happXHTTP := map[string]interface{}{
		"path":             effectivePath,
		"host":             domain,
		"mode":             xhttpMode,
		"uplinkHTTPMethod": method,
		"enableXmux":       true,
	}

	var tuning *XHTTPTuning
	if cfg.XHTTPTuning != nil {
		t := *cfg.XHTTPTuning
		_ = ValidateAndNormalizeXHTTPTuning(&t)
		tuning = &t

		if tuning.UplinkMethod != "" {
			method = tuning.UplinkMethod
			happXHTTP["uplinkHTTPMethod"] = method
		}
		if tuning.StreamPathSuffix != "" && !strings.HasSuffix(effectivePath, tuning.StreamPathSuffix) {
			effectivePath = strings.TrimRight(effectivePath, "/") + "/" + tuning.StreamPathSuffix
			happXHTTP["path"] = effectivePath
		}
		happXHTTP["enableXmux"] = tuning.EnableXmux
		if tuning.MinPostsIntervalMs != "" {
			happXHTTP["scMinPostsIntervalMs"] = tuning.MinPostsIntervalMs
		}
		if tuning.MaxBufferedPosts > 0 {
			happXHTTP["scMaxBufferedPosts"] = tuning.MaxBufferedPosts
		}
		if tuning.StreamUpServerSecs != "" {
			happXHTTP["streamUpServerSecs"] = tuning.StreamUpServerSecs
		}
		if tuning.XPaddingBytes != "" {
			happXHTTP["xPaddingBytes"] = tuning.XPaddingBytes
		}
		if tuning.XPaddingHeader != "" {
			happXHTTP["xPaddingHeader"] = tuning.XPaddingHeader
		}
		if tuning.XPaddingPlacement != "" {
			happXHTTP["xPaddingPlacement"] = tuning.XPaddingPlacement
		}
		if tuning.XPaddingKey != "" {
			happXHTTP["xPaddingKey"] = tuning.XPaddingKey
		}
		if tuning.XPaddingMethod != "" {
			happXHTTP["xPaddingMethod"] = tuning.XPaddingMethod
		}
		if tuning.XPaddingObfsMode {
			happXHTTP["xPaddingObfsMode"] = true
		}
		if tuning.ScMaxEachPostBytes > 0 {
			happXHTTP["scMaxEachPostBytes"] = tuning.ScMaxEachPostBytes
		}
		if tuning.SessionPlacement != "" {
			happXHTTP["sessionPlacement"] = tuning.SessionPlacement
			happXHTTP["sessionIDPlacement"] = tuning.SessionPlacement
		}
		if tuning.SessionKey != "" {
			happXHTTP["sessionKey"] = tuning.SessionKey
			happXHTTP["sessionIDKey"] = tuning.SessionKey
		}
		if tuning.SeqPlacement != "" {
			happXHTTP["seqPlacement"] = tuning.SeqPlacement
		}
		if tuning.SeqKey != "" {
			happXHTTP["seqKey"] = tuning.SeqKey
		}

		isStreamOne := strings.EqualFold(xhttpMode, "stream-one")
		isStreamUp := strings.EqualFold(xhttpMode, "stream-up")
		extraMap := map[string]interface{}{}
		if !isStreamOne || (method != "" && !strings.EqualFold(method, "POST")) {
			extraMap["uplinkHTTPMethod"] = method
		}
		if !isStreamOne {
			if tuning.MinPostsIntervalMs != "" {
				extraMap["scMinPostsIntervalMs"] = tuning.MinPostsIntervalMs
			}
			if tuning.MaxBufferedPosts > 0 {
				extraMap["scMaxBufferedPosts"] = tuning.MaxBufferedPosts
			}
			if isStreamUp && tuning.StreamUpServerSecs != "" {
				extraMap["scStreamUpServerSecs"] = tuning.StreamUpServerSecs
			}
			if tuning.ScMaxEachPostBytes > 0 && tuning.ScMaxEachPostBytes != 1000000 {
				extraMap["scMaxEachPostBytes"] = tuning.ScMaxEachPostBytes
			}
			if tuning.SessionPlacement != "" {
				extraMap["sessionIDPlacement"] = tuning.SessionPlacement
			}
			if tuning.SessionKey != "" {
				extraMap["sessionIDKey"] = tuning.SessionKey
			}
			if tuning.SeqPlacement != "" {
				extraMap["seqPlacement"] = tuning.SeqPlacement
			}
			if tuning.SeqKey != "" {
				extraMap["seqKey"] = tuning.SeqKey
			}
		}
		if tuning.EnableXmux {
			extraXmuxObj := map[string]interface{}{
				"maxConcurrency":   "8-16",
				"hMaxRequestTimes": "600-900",
				"hMaxReusableSecs": "1800-3000",
				"hKeepAlivePeriod": 10,
			}
			extraMap["xmux"] = extraXmuxObj
			happXHTTP["enableXmux"] = true
			happXHTTP["xmux"] = map[string]interface{}{
				"maxConcurrency":   "8-16",
				"cMaxReuseTimes":   0,
				"hMaxRequestTimes": "600-900",
				"hMaxReusableSecs": "1800-3000",
				"hKeepAlivePeriod": 10,
			}
		}
		if tuning.XPaddingBytes != "" {
			extraMap["xPaddingBytes"] = tuning.XPaddingBytes
		}
		if tuning.XPaddingHeader != "" {
			extraMap["xPaddingHeader"] = tuning.XPaddingHeader
		}
		if tuning.XPaddingPlacement != "" {
			extraMap["xPaddingPlacement"] = tuning.XPaddingPlacement
		}
		if tuning.XPaddingKey != "" {
			extraMap["xPaddingKey"] = tuning.XPaddingKey
		}
		if tuning.XPaddingMethod != "" {
			extraMap["xPaddingMethod"] = tuning.XPaddingMethod
		}
		if tuning.XPaddingObfsMode {
			extraMap["xPaddingObfsMode"] = true
		}
		if len(extraMap) > 0 {
			xhttpExtra, _ = json.Marshal(extraMap)
		}
	} else if method != "" && (!strings.EqualFold(xhttpMode, "stream-one") || !strings.EqualFold(method, "POST")) {
		xhttpExtra, _ = json.Marshal(map[string]string{"uplinkHTTPMethod": method})
	}

	extraQuery := ""
	if len(xhttpExtra) > 0 {
		extraQuery = "&extra=" + url.QueryEscape(string(xhttpExtra))
	}
	vlessURL := fmt.Sprintf(
		"vless://%s@%s:%d?encryption=none&security=tls&sni=%s&alpn=h2,http/1.1&type=xhttp&host=%s&path=%s&mode=%s&packetEncoding=xudp%s#%s",
		client.ID,
		domain,
		port,
		domain,
		domain,
		url.QueryEscape(effectivePath),
		xhttpMode,
		extraQuery,
		url.QueryEscape(client.Remark),
	)

	var finiteProtoURL string
	fpRemark := strings.TrimSpace(client.Remark)
	if fpRemark != "" {
		if !strings.Contains(strings.ToLower(fpRemark), "finite") {
			fpRemark += " (FiniteProto)"
		}
	} else {
		fpRemark = "FiniteProto"
	}
	if hasValidFPCredentials(client) {
		fpChunkBytes := 131072
		fpMaxWays := 1
		if tuning != nil {
			if tuning.FPChunkBytes >= 16384 && tuning.FPChunkBytes <= 262144 {
				fpChunkBytes = tuning.FPChunkBytes
			}
			if tuning.FPMaxWays >= 1 && tuning.FPMaxWays <= 8 {
				fpMaxWays = tuning.FPMaxWays
			}
		}
		finiteProtoURL = fmt.Sprintf(
			"vless://%s@%s:%d?encryption=none&security=tls&sni=%s&alpn=http/1.1&type=xhttp&host=%s&path=%s&mode=finiteproto&fp_v=2&fp_id=%s&fp_secret=%s&chunk_bytes=%d&max_ways=%d#%s",
			client.ID,
			domain,
			port,
			domain,
			domain,
			url.QueryEscape(effectivePath),
			url.QueryEscape(strings.TrimSpace(client.FPClientID)),
			url.QueryEscape(strings.ToLower(strings.TrimSpace(client.FPSecretHex))),
			fpChunkBytes,
			fpMaxWays,
			url.QueryEscape(fpRemark),
		)
	}
	if isFiniteProto {
		if finiteProtoURL == "" {
			return nil, errors.New("valid FiniteProto v2 credentials are required for finiteproto transport")
		}
		vlessURL = finiteProtoURL
	}

	happObj := map[string]interface{}{
		"protocol":        "vless",
		"address":         domain,
		"port":            port,
		"uuid":            client.ID,
		"security":        "tls",
		"sni":             domain,
		"alpn":            []string{"h2", "http/1.1"},
		"packet_encoding": "xudp",
		"packetEncoding":  "xudp",
		"network":         "xhttp",
		"xhttp":           happXHTTP,
		"remark":          client.Remark,
	}
	happJSON, _ := json.MarshalIndent(happObj, "", "  ")

	transportMap := map[string]interface{}{
		"type": "xhttp",
		"path": effectivePath,
		"host": domain,
		"mode": xhttpMode,
	}
	if method != "" {
		transportMap["uplink_http_method"] = method
	}
	if tuning != nil {
		if tuning.MinPostsIntervalMs != "" {
			transportMap["sc_min_posts_interval_ms"] = tuning.MinPostsIntervalMs
		}
		if tuning.MaxBufferedPosts > 0 {
			transportMap["sc_max_buffered_posts"] = tuning.MaxBufferedPosts
		}
		if tuning.XPaddingBytes != "" {
			transportMap["x_padding_bytes"] = tuning.XPaddingBytes
		}
		if tuning.XPaddingHeader != "" {
			transportMap["x_padding_header"] = tuning.XPaddingHeader
		}
		if tuning.XPaddingPlacement != "" {
			transportMap["x_padding_placement"] = tuning.XPaddingPlacement
		}
		if tuning.XPaddingKey != "" {
			transportMap["x_padding_key"] = tuning.XPaddingKey
		}
		if tuning.XPaddingMethod != "" {
			transportMap["x_padding_method"] = tuning.XPaddingMethod
		}
		if tuning.XPaddingObfsMode {
			transportMap["x_padding_obfs_mode"] = true
		}
		if tuning.ScMaxEachPostBytes > 0 {
			transportMap["sc_max_each_post_bytes"] = tuning.ScMaxEachPostBytes
		}
		if tuning.SessionPlacement != "" {
			transportMap["session_placement"] = tuning.SessionPlacement
		}
		if tuning.SessionKey != "" {
			transportMap["session_key"] = tuning.SessionKey
		}
		if tuning.SeqPlacement != "" {
			transportMap["seq_placement"] = tuning.SeqPlacement
		}
		if tuning.SeqKey != "" {
			transportMap["seq_key"] = tuning.SeqKey
		}
		if tuning.EnableXmux {
			transportMap["xmux"] = map[string]interface{}{
				"max_concurrency":     "8-16",
				"c_max_reuse_times":   0,
				"h_max_request_times": "600-900",
				"h_max_reusable_secs": "1800-3000",
				"h_keep_alive_period": 10,
			}
		}
	}

	singboxObj := map[string]interface{}{
		"type":            "vless",
		"tag":             "proxy-cdn",
		"server":          domain,
		"server_port":     port,
		"uuid":            client.ID,
		"packet_encoding": "xudp",
		"tls": map[string]interface{}{
			"enabled":     true,
			"server_name": domain,
			"alpn":        []string{"h2", "http/1.1"},
		},
		"transport": transportMap,
	}
	singboxJSON, _ := json.MarshalIndent(singboxObj, "", "  ")

	mihomoYAML := fmt.Sprintf(`- name: "%s"
  type: vless
  server: %s
  port: %d
  uuid: %s
  cipher: auto
  tls: true
  servername: %s
  alpn:
    - h2
    - http/1.1
  packet-encoding: xudp
  network: xhttp
  xhttp-opts:
    mode: %s
    path: "%s"
    headers:
      Host: "%s"`, client.Remark, domain, port, client.ID, domain, xhttpMode, effectivePath, domain)

	return &ShareLinks{
		VlessURL:       vlessURL,
		FiniteProtoURL: finiteProtoURL,
		HappJSON:       string(happJSON),
		SingboxJSON:    string(singboxJSON),
		MihomoYAML:     mihomoYAML,
		Remark:         client.Remark,
		UUID:           client.ID,
	}, nil
}
