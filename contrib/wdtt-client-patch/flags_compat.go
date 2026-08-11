package main

import "flag"

// Совместимость с awg-manager (qWDTT 1.4 APK флаги).
var (
	compatVkAuthMode  = flag.String("vk-auth-mode", "", "alias для -vk-auth (awg-manager)")
	compatFingerprint = flag.String("fingerprint", "", "совместимость с awg-manager (игнорируется)")
	compatTunName     = flag.String("tun-name", "", "kernel TUN (rawtun fallback без -tun-fd-sock)")
	compatTunFdSock   = flag.String("tun-fd-sock", "", "unix-socket для TUN fd от awg-manager (как APK -tun-fd-sock)")
)

func applyCompatFlags(vkAuth *string, vkAnonPath *string) {
	if compatFingerprint != nil && *compatFingerprint != "" {
		_ = compatFingerprint // TLS fingerprint задаётся внутри vk client
	}
	if compatVkAuthMode == nil || *compatVkAuthMode == "" {
		return
	}
	mode := *compatVkAuthMode
	switch mode {
	case "vkcalls":
		*vkAuth = "anonymous"
		*vkAnonPath = "vkcalls"
	case "legacy":
		*vkAuth = "anonymous"
		*vkAnonPath = "legacy"
	case "anonymous", "account":
		*vkAuth = mode
	default:
		*vkAuth = mode
	}
}
