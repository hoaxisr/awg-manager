package aiassistant

import (
	"errors"
	"fmt"
	"regexp"
	"strings"
)

var (
	// Redirection operators that could write to disk or devices.
	redirectPattern = regexp.MustCompile(`(?:^|[^<])>{1,2}|&>|>\||<(?:\s*\(|\s*&|\s*>)`)

	// Mutating / destructive binaries and shell builtins.
	blockedBinaryPattern = regexp.MustCompile(`(?i)(?:^|[\s;|&` + "`" + `])(rm|mkfs|mke2fs|reboot|halt|poweroff|shutdown|init\s+[06]|mv|cp|chmod|chown|chgrp|kill|pkill|killall|dd|truncate|touch|unlink|shred|wget\s+.*-(?:O|P)|curl\s+.*-(?:o|O)|tee)(?:[\s;|&` + "`" + `]|$)`)

	// In-place file edits with sed.
	sedInPlacePattern = regexp.MustCompile(`(?i)\bsed\s+.*-[a-z]*i`)

	// Mutating netfilter / iptables flags (-A, -I, -D, -F, -R, -Z, -N, -X).
	iptablesMutatePattern = regexp.MustCompile(`(?i)\b(?:ip6?tables|iptables-legacy|ip6tables-legacy)\s+.*-(?:[AIDFRZNX]|new-chain|delete-chain|flush|zero|append|insert|delete|replace)\b`)

	// Mutating nft commands (add, delete, flush, insert, replace).
	nftMutatePattern = regexp.MustCompile(`(?i)\bnft\s+.*(?:add|delete|flush|insert|replace)\b`)

	// Mutating opkg actions (install, remove, upgrade, flag).
	opkgMutatePattern = regexp.MustCompile(`(?i)\bopkg\s+.*(?:install|remove|upgrade|flag)\b`)

	// Secret patterns to redact from command outputs.
	privateKeyPattern    = regexp.MustCompile(`(?i)(private[_-]?key|preshared[_-]?key|secret|token|password|auth|authorization)\s*[:=]\s*([A-Za-z0-9+/=_-]{16,})`)
	wgKeyInOutputPattern = regexp.MustCompile(`(?i)(private key:\s*)([A-Za-z0-9+/]{42}[AEIMQUYcgkosw048]=)`)
	wgPskInOutputPattern = regexp.MustCompile(`(?i)(preshared key:\s*)([A-Za-z0-9+/]{42}[AEIMQUYcgkosw048]=)`)
	bearerPattern        = regexp.MustCompile(`(?i)bearer\s+[A-Za-z0-9._~+/-]+=*`)
)

// ValidateDiagnosticCommand checks if the command is strictly read-only and safe for diagnosis.
func ValidateDiagnosticCommand(cmd string) error {
	trimmed := strings.TrimSpace(cmd)
	if trimmed == "" {
		return errors.New("диагностическая команда не может быть пустой")
	}

	if redirectPattern.MatchString(trimmed) {
		return errors.New("перенаправление вывода на запись (>, >>, tee) запрещено в режиме диагностики")
	}

	if match := blockedBinaryPattern.FindStringSubmatch(trimmed); len(match) > 1 {
		return fmt.Errorf("команда содержит запрещённую мутирующую операцию %q: режим диагностики строго Read-Only", match[1])
	}

	if sedInPlacePattern.MatchString(trimmed) {
		return errors.New("модификация файлов на месте (sed -i) запрещена в режиме диагностики")
	}

	if iptablesMutatePattern.MatchString(trimmed) {
		return errors.New("изменение правил фаервола (iptables -A/-I/-D/-F) запрещено: разрешены только флаги инспекции (-L, -S, -n, -v, -t)")
	}

	if nftMutatePattern.MatchString(trimmed) {
		return errors.New("изменение правил nftables запрещено: разрешены только команды инспекции (nft list ...)")
	}

	if opkgMutatePattern.MatchString(trimmed) {
		return errors.New("изменение пакетов opkg запрещено в диагностике: для установки используйте remediation.propose")
	}

	return nil
}

// SanitizeDiagnosticOutput cleans secret tokens/keys and enforces length bounds.
func SanitizeDiagnosticOutput(stdout, stderr string, maxLen int) string {
	combined := stdout
	if strings.TrimSpace(stderr) != "" {
		if strings.TrimSpace(combined) != "" {
			combined += "\n[STDERR]: " + stderr
		} else {
			combined = "[STDERR]: " + stderr
		}
	}

	combined = privateKeyPattern.ReplaceAllString(combined, "$1: [REDACTED]")
	combined = wgKeyInOutputPattern.ReplaceAllString(combined, "$1(hidden)")
	combined = wgPskInOutputPattern.ReplaceAllString(combined, "$1(hidden)")
	combined = bearerPattern.ReplaceAllString(combined, "Bearer [REDACTED]")

	combined = strings.ReplaceAll(combined, "\r", "")
	combined = strings.TrimSpace(combined)

	if maxLen <= 0 {
		maxLen = 12000
	}

	if len(combined) > maxLen {
		half := maxLen / 2
		return combined[:half] + fmt.Sprintf("\n\n... [пропущено %d байт вывода] ...\n\n", len(combined)-maxLen) + combined[len(combined)-half:]
	}

	return combined
}
