package mihomonative

import "strings"

// CheckVLESS reports compiler compatibility, not merely upstream-core
// capability. This keeps the UI honest when AWG Manager has not implemented
// a transport yet.
func CheckVLESS(transport string) Compatibility {
	transport = strings.ToLower(strings.TrimSpace(transport))
	if transport == "" {
		transport = "tcp"
	}
	mihomo := map[string]bool{"tcp": true, "ws": true, "http": true, "h2": true, "grpc": true, "xhttp": true}
	singbox := map[string]bool{"tcp": true, "ws": true, "http": true, "h2": true, "grpc": true}
	result := Compatibility{
		Mihomo:  Support{Supported: mihomo[transport]},
		Singbox: Support{Supported: singbox[transport]},
	}
	if !result.Mihomo.Supported {
		result.Mihomo.Reason = "VLESS transport " + transport + " is not implemented by the Mihomo compiler"
	}
	if !result.Singbox.Supported {
		result.Singbox.Reason = "VLESS transport " + transport + " is not implemented by the sing-box compiler"
	}
	return result
}

// CheckVMess reports compiler compatibility for VMess protocol.
func CheckVMess(transport string) Compatibility {
	transport = strings.ToLower(strings.TrimSpace(transport))
	if transport == "" {
		transport = "tcp"
	}
	mihomo := map[string]bool{"tcp": true, "ws": true, "http": true, "h2": true, "grpc": true, "xhttp": true, "kcp": true}
	supported := mihomo[transport]
	result := Compatibility{
		Mihomo:  Support{Supported: supported},
		Singbox: Support{Supported: false, Reason: "VMess is supported natively in Mihomo"},
	}
	if !supported {
		result.Mihomo.Reason = "VMess transport " + transport + " is not implemented by the Mihomo compiler"
	}
	return result
}

// CheckSocks reports compiler compatibility for SOCKS proxy.
func CheckSocks() Compatibility {
	return Compatibility{
		Mihomo:  Support{Supported: true},
		Singbox: Support{Supported: false, Reason: "SOCKS proxy is supported natively in Mihomo"},
	}
}

func SelectEngine(preference EnginePreference, compatibility Compatibility, routingEngine EnginePreference) (EnginePreference, string) {
	if preference == "" {
		preference = EngineAuto
	}
	if preference == EngineMihomo {
		if compatibility.Mihomo.Supported {
			return EngineMihomo, ""
		}
		return "", compatibility.Mihomo.Reason
	}
	if preference == EngineSingbox {
		if compatibility.Singbox.Supported {
			return EngineSingbox, ""
		}
		return "", compatibility.Singbox.Reason
	}
	if routingEngine == EngineMihomo && compatibility.Mihomo.Supported {
		return EngineMihomo, ""
	}
	if routingEngine == EngineSingbox && compatibility.Singbox.Supported {
		return EngineSingbox, ""
	}
	if compatibility.Mihomo.Supported {
		return EngineMihomo, ""
	}
	if compatibility.Singbox.Supported {
		return EngineSingbox, ""
	}
	return "", "proxy is not supported by either compiler"
}
