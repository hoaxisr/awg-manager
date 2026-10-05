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

	// Mutating / destructive binaries and shell builtins, including ndmc.
	blockedBinaryPattern = regexp.MustCompile(`(?i)(?:^|[\s;|&` + "`" + `])(rm|mkfs|mke2fs|reboot|halt|poweroff|shutdown|init\s+[06]|mv|cp|chmod|chown|chgrp|kill|pkill|killall|dd|truncate|touch|unlink|shred|wget\s+.*-(?:O|P)|curl\s+.*-(?:o|O)|tee|ndmc)(?:[\s;|&` + "`" + `]|$)`)

	// In-place file edits with sed.
	sedInPlacePattern = regexp.MustCompile(`(?i)\bsed\s+.*-[a-z]*i`)

	// Mutating netfilter / iptables flags (-A, -I, -D, -F, -R, -Z, -N, -X).
	iptablesMutatePattern = regexp.MustCompile(`(?i)\b(?:ip6?tables|iptables-legacy|ip6tables-legacy)\s+.*-(?:[AIDFRZNX]|new-chain|delete-chain|flush|zero|append|insert|delete|replace)\b`)

	// Mutating nft commands (add, delete, flush, insert, replace).
	nftMutatePattern = regexp.MustCompile(`(?i)\bnft\s+.*(?:add|delete|flush|insert|replace)\b`)

	// Mutating opkg actions (install, remove, upgrade, flag).
	opkgMutatePattern = regexp.MustCompile(`(?i)\bopkg\s+.*(?:install|remove|upgrade|flag)\b`)

	// Mutating ip subcommands (add, del, delete, change, replace, set, flush, etc.)
	ipMutatePattern = regexp.MustCompile(`(?i)\bip(?:\s+-(?:4|6|o|d|s|br))*?\s+(?:link\s+set|route\s+(?:add|del|delete|change|replace|flush|append)|rule\s+(?:add|del|delete|flush)|addr(?:ess)?\s+(?:add|del|delete|change|replace|flush)|neigh(?:bor)?\s+(?:add|del|delete|change|replace|flush))\b`)

	// Mutating curl options (POST, PUT, DELETE, PATCH, data payloads, form uploads)
	curlMutatePattern = regexp.MustCompile(`(?i)\bcurl\b.*?(?:-X\s*(?:POST|PUT|DELETE|PATCH)|--request\s*(?:POST|PUT|DELETE|PATCH)|-d\b|--data\b|--data-raw\b|--data-binary\b|--data-urlencode\b|-F\b|--form\b)`)

	// Localhost RCI mutations (any POST/PUT/DELETE to 127.0.0.1:79/rci or localhost/rci)
	rciMutatePattern = regexp.MustCompile(`(?i)(?:127\.0\.0\.1|localhost)(?::79)?/rci/.*?(?:-X|-d|--data)`)

	// Whitelisted primary diagnostic commands
	whitelistedPrimaryPatterns = []*regexp.Regexp{
		regexp.MustCompile(`(?i)^ip(?:\s+-(?:4|6|o|d|s|br|t))*\s+(?:(?:addr(?:ess)?|a)\s+show|(?:route|r)\s+(?:show|get)|(?:rule|ru)\s+show|(?:link|l)\s+show|(?:neigh(?:bor)?)\s+show)(?:\s+.*)?$`),
		regexp.MustCompile(`(?i)^ping(?:6)?\s+.*-c\s+\d+.*$`),
		regexp.MustCompile(`(?i)^traceroute(?:6)?\b.*$`),
		regexp.MustCompile(`(?i)^ss(?:\s+-[A-Za-z0-9]+)*(?:\s+.*)?$`),
		regexp.MustCompile(`(?i)^netstat(?:\s+-[A-Za-z0-9]+)*(?:\s+.*)?$`),
		regexp.MustCompile(`(?i)^dmesg\b.*$`),
		regexp.MustCompile(`(?i)^logread\b.*$`),
		regexp.MustCompile(`(?i)^curl\b.*$`),
		regexp.MustCompile(`(?i)^cat\s+(?:/proc/|/sys/|/etc/)[A-Za-z0-9._/-]+.*$`),
		regexp.MustCompile(`(?i)^ps(?:\s+[a-zA-Z0-9_-]+)*(?:\s+.*)?$`),
		regexp.MustCompile(`(?i)^top\s+.*-b\b.*$`),
		regexp.MustCompile(`(?i)^free(?:\s+-[a-zA-Z0-9]+)*(?:\s+.*)?$`),
		regexp.MustCompile(`(?i)^df(?:\s+-[a-zA-Z0-9]+)*(?:\s+.*)?$`),
		regexp.MustCompile(`(?i)^nslookup\b.*$`),
		regexp.MustCompile(`(?i)^dig\b.*$`),
		regexp.MustCompile(`(?i)^(?:ip6?tables|iptables-legacy|ip6tables-legacy)\s+.*-(?:[LSnvt]|list|numeric|verbose)\b.*$`),
		regexp.MustCompile(`(?i)^nft\s+list\b.*$`),
		regexp.MustCompile(`(?i)^(?:awg|wg)\s+show\b.*$`),
		regexp.MustCompile(`(?i)^conntrack\s+-L\b.*$`),
		regexp.MustCompile(`(?i)^/opt/etc/init\.d/\S+\s+status\b.*$`),
		regexp.MustCompile(`(?i)^opkg\s+(?:list(?:-installed)?|status|info|find|depends|whatdepends)\b.*$`),
	}

	// Whitelisted pipe filter commands
	whitelistedPipePattern = regexp.MustCompile(`(?i)^(?:grep|egrep|fgrep|tail|head|awk|wc|cat|cut|sort|uniq|tr|sed|jq)\b.*$`)

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

	// Reject command chaining operators
	if strings.ContainsAny(trimmed, ";`$") || strings.Contains(trimmed, "&&") || strings.Contains(trimmed, "||") {
		return errors.New("объединение команд (;, &&, ||, subshell) запрещено в режиме диагностики")
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

	if ipMutatePattern.MatchString(trimmed) {
		return errors.New("мутирующие операции ip (add, del, set, change, replace, flush) запрещены: разрешены только команды инспекции show/get")
	}

	if curlMutatePattern.MatchString(trimmed) {
		return errors.New("мутирующие HTTP-запросы curl (POST, PUT, DELETE, data payloads) запрещены в режиме диагностики")
	}

	if rciMutatePattern.MatchString(trimmed) {
		return errors.New("мутирующие вызовы к Keenetic RCI API запрещены в режиме диагностики")
	}

	// Split by pipeline
	pipes := strings.Split(trimmed, "|")
	primary := strings.TrimSpace(pipes[0])

	matched := false
	for _, pattern := range whitelistedPrimaryPatterns {
		if pattern.MatchString(primary) {
			matched = true
			break
		}
	}
	if !matched {
		return fmt.Errorf("команда %q не входит в белый список разрешённых диагностических утилит", primary)
	}

	// Verify all subsequent pipeline segments
	for i := 1; i < len(pipes); i++ {
		segment := strings.TrimSpace(pipes[i])
		if segment == "" {
			return errors.New("пустой сегмент пайплайна")
		}
		if !whitelistedPipePattern.MatchString(segment) {
			return fmt.Errorf("утилита в пайплайне %q не входит в белый список разрешённых фильтров", segment)
		}
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
