package vlink

import (
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"net"
	"net/url"
	"regexp"
	"strconv"
	"strings"

	pb "github.com/enfein/mieru/v3/pkg/appctl/appctlpb"
	"google.golang.org/protobuf/proto"
)

type mieruPortSpec struct {
	Value   string
	Numeric bool
}

func parseMieruStandard(input string) ([]ParsedOutbound, error) {
	const prefix = "mieru://"
	if !strings.HasPrefix(strings.ToLower(input), prefix) {
		return nil, errors.New("mieru: missing mieru:// prefix")
	}
	body := strings.TrimSpace(input[len(prefix):])
	if body == "" {
		return nil, errors.New("mieru: empty config payload")
	}
	if i := strings.IndexByte(body, '#'); i >= 0 {
		body = body[:i]
	}
	decoded, err := base64.StdEncoding.DecodeString(body)
	if err != nil {
		return nil, fmt.Errorf("mieru: base64: %w", err)
	}
	cfg := &pb.ClientConfig{}
	if err := proto.Unmarshal(decoded, cfg); err != nil {
		return nil, fmt.Errorf("mieru: protobuf client config: %w", err)
	}
	profile, err := selectMieruProfile(cfg)
	if err != nil {
		return nil, err
	}
	return mieruProfileToOutbounds(profile)
}

func parseMieruSimple(input string) ([]ParsedOutbound, error) {
	u, err := url.Parse(input)
	if err != nil {
		return nil, fmt.Errorf("mierus: parse: %w", err)
	}
	if u.Scheme != "mierus" {
		return nil, fmt.Errorf("mierus: unexpected scheme %q", u.Scheme)
	}
	if u.Opaque != "" {
		return nil, errors.New("mierus: opaque URL is not supported")
	}
	if u.User == nil || u.User.Username() == "" {
		return nil, errors.New("mierus: missing username")
	}
	password, _ := u.User.Password()
	if password == "" {
		return nil, errors.New("mierus: missing password")
	}
	host := u.Hostname()
	if host == "" {
		return nil, errors.New("mierus: missing host")
	}
	q, err := url.ParseQuery(u.RawQuery)
	if err != nil {
		return nil, fmt.Errorf("mierus: query: %w", err)
	}
	if q.Get("profile") == "" {
		return nil, errors.New("mierus: missing profile")
	}
	ports := q["port"]
	protocols := q["protocol"]
	if len(ports) == 0 {
		return nil, errors.New("mierus: missing port")
	}
	if len(protocols) == 1 && len(ports) > 1 {
		// Некоторые панели кладут один protocol на все port'ы (issue #516).
		for len(protocols) < len(ports) {
			protocols = append(protocols, protocols[0])
		}
	}
	if len(ports) != len(protocols) {
		return nil, errors.New("mierus: mismatched number of port and protocol parameters")
	}

	profile := &pb.ClientProfile{
		ProfileName: proto.String(q.Get("profile")),
		User: &pb.User{
			Name:     proto.String(u.User.Username()),
			Password: proto.String(password),
		},
	}
	server := &pb.ServerEndpoint{}
	if net.ParseIP(host) != nil {
		server.IpAddress = proto.String(host)
	} else {
		server.DomainName = proto.String(host)
	}
	for i, rawPort := range ports {
		binding, err := mieruPortBindingFromStrings(rawPort, protocols[i])
		if err != nil {
			return nil, fmt.Errorf("mierus: %w", err)
		}
		server.PortBindings = append(server.PortBindings, binding)
	}
	profile.Servers = []*pb.ServerEndpoint{server}
	if mux := q.Get("multiplexing"); mux != "" {
		level, ok := pb.MultiplexingLevel_value[mux]
		if !ok {
			return nil, fmt.Errorf("mierus: invalid multiplexing %q", mux)
		}
		profile.Multiplexing = &pb.MultiplexingConfig{
			Level: pb.MultiplexingLevel(level).Enum(),
		}
	}
	if tpRaw := q.Get("traffic-pattern"); tpRaw != "" {
		decoded, err := base64.StdEncoding.DecodeString(tpRaw)
		if err != nil {
			return nil, fmt.Errorf("mierus: traffic-pattern base64: %w", err)
		}
		tp := &pb.TrafficPattern{}
		if err := proto.Unmarshal(decoded, tp); err != nil {
			return nil, fmt.Errorf("mierus: traffic-pattern protobuf: %w", err)
		}
		profile.TrafficPattern = tp
	}
	return mieruProfileToOutbounds(profile)
}

func selectMieruProfile(cfg *pb.ClientConfig) (*pb.ClientProfile, error) {
	profiles := cfg.GetProfiles()
	if len(profiles) == 0 {
		return nil, errors.New("mieru: client config has no profiles")
	}
	active := strings.TrimSpace(cfg.GetActiveProfile())
	if active == "" {
		return profiles[0], nil
	}
	for _, profile := range profiles {
		if profile.GetProfileName() == active {
			return profile, nil
		}
	}
	return nil, fmt.Errorf("mieru: active profile %q not found", active)
}

func mieruProfileToOutbounds(profile *pb.ClientProfile) ([]ParsedOutbound, error) {
	if profile == nil {
		return nil, errors.New("mieru: nil profile")
	}
	username := profile.GetUser().GetName()
	password := profile.GetUser().GetPassword()
	if username == "" {
		return nil, errors.New("mieru: missing username")
	}
	if password == "" {
		return nil, errors.New("mieru: missing password")
	}
	servers := profile.GetServers()
	if len(servers) == 0 {
		return nil, errors.New("mieru: profile has no servers")
	}
	out := make([]ParsedOutbound, 0, len(servers))
	for serverIdx, server := range servers {
		host := firstNonEmpty(server.GetDomainName(), server.GetIpAddress())
		if host == "" {
			return nil, fmt.Errorf("mieru: server %d has no address", serverIdx+1)
		}
		grouped := map[string][]mieruPortSpec{}
		for _, binding := range server.GetPortBindings() {
			transport := binding.GetProtocol().String()
			if transport != "TCP" && transport != "UDP" {
				return nil, fmt.Errorf("mieru: unsupported transport %q", transport)
			}
			spec, err := mieruPortSpecFromBinding(binding)
			if err != nil {
				return nil, fmt.Errorf("mieru: %w", err)
			}
			grouped[transport] = append(grouped[transport], spec)
		}
		for _, transport := range []string{"TCP", "UDP"} {
			specs := grouped[transport]
			if len(specs) == 0 {
				continue
			}
			parsed, err := buildMieruOutbound(profile, host, username, password, transport, specs)
			if err != nil {
				return nil, err
			}
			out = append(out, *parsed)
		}
	}
	if len(out) == 0 {
		return nil, errors.New("mieru: no usable port bindings")
	}
	return out, nil
}

// mieruParams — нормализованные значения одного mieru-сервера, откуда бы он
// ни приехал: из mieru://, из Clash YAML или из client-JSON. Сборку карты
// аутбаунда держит одна функция, иначе формы снова разъедутся.
type mieruParams struct {
	host           string
	username       string
	password       string
	transport      string
	specs          []mieruPortSpec
	multiplexing   string
	trafficPattern string
	label          string
}

func assembleMieruOutbound(p mieruParams) (*ParsedOutbound, error) {
	out := map[string]any{
		"type":      "mieru",
		"server":    p.host,
		"transport": p.transport,
		"username":  p.username,
		"password":  p.password,
	}
	var primaryPort uint16
	var serverPorts []string
	for _, spec := range p.specs {
		if spec.Numeric && primaryPort == 0 {
			n, _ := strconv.ParseUint(spec.Value, 10, 16)
			primaryPort = uint16(n)
			out["server_port"] = int(n)
			continue
		}
		serverPorts = append(serverPorts, spec.Value)
	}
	if len(serverPorts) > 0 {
		out["server_ports"] = serverPorts
	}
	if _, hasPort := out["server_port"]; !hasPort && len(serverPorts) == 0 {
		return nil, errors.New("mieru: no server port")
	}
	if p.multiplexing != "" {
		out["multiplexing"] = p.multiplexing
	}
	if p.trafficPattern != "" {
		out["traffic_pattern"] = p.trafficPattern
	}

	tag := fmt.Sprintf("mieru-%s-%s-%s", sanitizeTagPart(p.host), strings.ToLower(p.transport), mieruPortTagPart(primaryPort, p.specs))
	out["tag"] = tag
	raw, err := json.Marshal(out)
	if err != nil {
		return nil, err
	}
	return &ParsedOutbound{
		Tag:      tag,
		Protocol: "mieru",
		Server:   p.host,
		Port:     primaryPort,
		Outbound: raw,
		Label:    p.label,
	}, nil
}

func buildMieruOutbound(profile *pb.ClientProfile, host, username, password, transport string, specs []mieruPortSpec) (*ParsedOutbound, error) {
	params := mieruParams{
		host:      host,
		username:  username,
		password:  password,
		transport: transport,
		specs:     specs,
		label:     profile.GetProfileName(),
	}
	if mux := profile.GetMultiplexing(); mux != nil && mux.Level != nil {
		params.multiplexing = mux.GetLevel().String()
	}
	if tp := profile.GetTrafficPattern(); tp != nil {
		encoded, err := encodeMieruTrafficPattern(tp)
		if err != nil {
			return nil, err
		}
		params.trafficPattern = encoded
	}
	return assembleMieruOutbound(params)
}

func encodeMieruTrafficPattern(tp *pb.TrafficPattern) (string, error) {
	raw, err := proto.Marshal(tp)
	if err != nil {
		return "", fmt.Errorf("mieru: traffic-pattern marshal: %w", err)
	}
	return base64.StdEncoding.EncodeToString(raw), nil
}

func mieruPortBindingFromStrings(rawPort, rawProtocol string) (*pb.PortBinding, error) {
	protocol, ok := pb.TransportProtocol_value[rawProtocol]
	if !ok || (rawProtocol != "TCP" && rawProtocol != "UDP") {
		return nil, fmt.Errorf("invalid protocol %q", rawProtocol)
	}
	if strings.Contains(rawPort, "-") {
		normalized, err := normalizeMieruPortRange(rawPort)
		if err != nil {
			return nil, err
		}
		return &pb.PortBinding{
			PortRange: proto.String(normalized),
			Protocol:  pb.TransportProtocol(protocol).Enum(),
		}, nil
	}
	port, err := parseMieruPort(rawPort)
	if err != nil {
		return nil, err
	}
	return &pb.PortBinding{
		Port:     proto.Int32(int32(port)),
		Protocol: pb.TransportProtocol(protocol).Enum(),
	}, nil
}

func mieruPortSpecFromBinding(binding *pb.PortBinding) (mieruPortSpec, error) {
	if binding.GetPortRange() != "" {
		rng, err := normalizeMieruPortRange(binding.GetPortRange())
		if err != nil {
			return mieruPortSpec{}, err
		}
		return mieruPortSpec{Value: rng}, nil
	}
	port := binding.GetPort()
	if port < 1 || port > 65535 {
		return mieruPortSpec{}, fmt.Errorf("invalid port %d", port)
	}
	return mieruPortSpec{Value: strconv.Itoa(int(port)), Numeric: true}, nil
}

func parseMieruPort(raw string) (int, error) {
	port, err := strconv.Atoi(strings.TrimSpace(raw))
	if err != nil || port < 1 || port > 65535 {
		return 0, fmt.Errorf("invalid port %q", raw)
	}
	return port, nil
}

func normalizeMieruPortRange(raw string) (string, error) {
	parts := strings.Split(raw, "-")
	if len(parts) != 2 {
		return "", fmt.Errorf("invalid port range %q", raw)
	}
	begin, err := parseMieruPort(parts[0])
	if err != nil {
		return "", err
	}
	end, err := parseMieruPort(parts[1])
	if err != nil {
		return "", err
	}
	if begin > end {
		return "", fmt.Errorf("invalid port range %q", raw)
	}
	return fmt.Sprintf("%d-%d", begin, end), nil
}

func mieruPortTagPart(primary uint16, specs []mieruPortSpec) string {
	if primary != 0 {
		return strconv.Itoa(int(primary))
	}
	if len(specs) > 0 {
		return sanitizeTagPart(specs[0].Value)
	}
	return "ports"
}

var tagUnsafeRe = regexp.MustCompile(`[^A-Za-z0-9_.-]+`)

func sanitizeTagPart(s string) string {
	s = strings.Trim(tagUnsafeRe.ReplaceAllString(s, "-"), "-")
	if s == "" {
		return "x"
	}
	return s
}
