package aiassistant

import (
	"regexp"
	"strings"
)

type Intent struct {
	Kind   string            `json:"kind"`
	Entity map[string]string `json:"entity,omitempty"`
}

var domainPattern = regexp.MustCompile(`(?i)(?:https?://)?([a-z0-9](?:[a-z0-9-]{0,61}[a-z0-9])?(?:\.[a-z0-9](?:[a-z0-9-]{0,61}[a-z0-9])?)+)`)

func DetectIntent(question string) Intent {
	trimmed := strings.TrimSpace(question)
	match := domainPattern.FindStringSubmatch(trimmed)
	if len(match) > 1 {
		domain := strings.ToLower(match[1])
		if strings.Contains(domain, ".") && !strings.HasPrefix(domain, ".") && !strings.HasSuffix(domain, ".") {
			return Intent{Kind: "domain.inspect", Entity: map[string]string{"domain": domain}}
		}
	}

	lower := strings.ToLower(trimmed)

	// Greetings and conversational questions stay in "chat" so autonomousChatAnswer can handle them
	if containsAny(lower, "привет", "здравствуй", "добрый день", "добрый вечер", "доброе утро", "старт", "start", "hello", "hi", "хай") {
		return Intent{Kind: "chat"}
	}
	if containsAny(lower, "что ты умеешь", "что умеешь", "помощь", "справка", "help", "возможност", "как пользоват") {
		return Intent{Kind: "chat"}
	}
	if containsAny(lower, "какие отклонен", "покажи отклонен", "список отклонен", "какие ошибки", "покажи ошибки", "что не так") {
		return Intent{Kind: "chat"}
	}

	// Specific diagnostic checks with direct tools
	if containsAny(lower, "проверь туннел", "проверить туннел", "статус туннел", "список туннел", "состояние туннел", "диагностика туннел") {
		return Intent{Kind: "tunnels.check"}
	}
	if containsAny(lower, "проверь dns", "проверить dns", "тест dns", "проверь днс", "тест днс", "статус dns", "состояние dns") {
		return Intent{Kind: "dns.check"}
	}
	if containsAny(lower, "статус маршрутизац", "проверь маршрутизац", "маршрутизация", "состояние движк", "какой движок", "движок", "слоты маршрутизац") {
		return Intent{Kind: "routing.check"}
	}
	if containsAny(lower, "статус системы", "состояние роутера", "нагрузка cpu", "память роутера", "процессы роутера") {
		return Intent{Kind: "system.check"}
	}

	// General diagnostic fallback
	diagnosticWords := []string{
		"проверь", "диагност", "не работает", "нет интернет", "ошиб", "сломал", "почини",
		"маршрут", "dns", "туннел", "прокси", "подписк", "subscription", "sing-box", "singbox", "mihomo", "wireguard", "порт",
	}
	for _, word := range diagnosticWords {
		if strings.Contains(lower, word) {
			return Intent{Kind: "diagnostics.general"}
		}
	}
	return Intent{Kind: "chat"}
}

func containsAny(s string, substrs ...string) bool {
	for _, sub := range substrs {
		if strings.Contains(s, sub) {
			return true
		}
	}
	return false
}
