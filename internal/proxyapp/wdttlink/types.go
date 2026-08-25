package wdttlink

import (
	"encoding/json"
	"fmt"
	"strings"
)

// ImportPayload — нормализованный результат разбора wdtt:// / qwdtt:// /
// подписки. json-имена — ВЕРБАТИМ со старого wdtt.ImportPayload (types.go:352):
// поле `payload` ответа импорта фронт читает по этим именам.
type ImportPayload struct {
	Name     string   `json:"name,omitempty"`
	Peer     string   `json:"peer"`
	Password string   `json:"password"`
	VKHashes []string `json:"vkHashes"`
	Workers  int      `json:"workers,omitempty"`
	Listen   string   `json:"listen,omitempty"`
	SubURL   string   `json:"subUrl,omitempty"`
	DeviceID string   `json:"deviceId,omitempty"`
	WG       string   `json:"wg,omitempty"` // optional bundled WireGuard client config
	ConnMode string   `json:"connMode,omitempty"`
}

// SubscriptionPreview — многопрофильный документ подписки (_wdtt.json).
type SubscriptionPreview struct {
	Name           string          `json:"name"`
	Description    string          `json:"description,omitempty"`
	TrafficUsedMb  float64         `json:"trafficUsedMb,omitempty"`
	TrafficLimitMb float64         `json:"trafficLimitMb,omitempty"`
	UpdatedAt      string          `json:"updatedAt,omitempty"`
	SubURL         string          `json:"subUrl"`
	Profiles       []ImportPayload `json:"profiles"`
}

// LinkDecodeResult — тело ответа ручки decode. Форма ВЕРБАТИМ прежняя
// (wdtt.LinkDecodeResult): фронт различает одиночный профиль и подписку по
// наличию ключей.
//
// Тип живёт ЗДЕСЬ, а не в пакете подписок, потому что его возвращает DecodeLink:
// разбор подписочного документа — часть разбора ссылки, и вынести его отдельно
// значило бы разорвать пакет пополам.
type LinkDecodeResult struct {
	Profile      *ImportPayload       `json:"profile,omitempty"`
	Subscription *SubscriptionPreview `json:"subscription,omitempty"`
}

func parseSubscriptionDocument(data []byte, subURL string) (SubscriptionPreview, error) {
	var raw map[string]interface{}
	if err := json.Unmarshal(data, &raw); err != nil {
		return SubscriptionPreview{}, err
	}
	profilesRaw, ok := raw["profiles"].([]interface{})
	if !ok || len(profilesRaw) == 0 {
		return SubscriptionPreview{}, fmt.Errorf("в подписке нет profiles")
	}
	var profiles []ImportPayload
	for _, item := range profilesRaw {
		m, ok := item.(map[string]interface{})
		if !ok {
			continue
		}
		p, err := mapJSONProfile(m)
		if err != nil {
			continue
		}
		if subURL != "" && p.SubURL == "" {
			p.SubURL = subURL
		}
		profiles = append(profiles, p)
	}
	if len(profiles) == 0 {
		return SubscriptionPreview{}, fmt.Errorf("не удалось разобрать profiles подписки")
	}
	sub := SubscriptionPreview{
		Name:           firstStr(raw, "subscriptionName", "name", "title"),
		Description:    firstStr(raw, "description", "desc"),
		UpdatedAt:      firstStr(raw, "updatedAt", "updated_at"),
		SubURL:         normalizeSubURL(subURL),
		Profiles:       profiles,
		TrafficUsedMb:  floatFrom(raw, "trafficUsedMb", "traffic_used_mb"),
		TrafficLimitMb: floatFrom(raw, "trafficLimitMb", "traffic_limit_mb"),
	}
	if sub.Name == "" {
		sub.Name = "WDTT подписка"
	}
	return sub, nil
}

func floatFrom(m map[string]interface{}, keys ...string) float64 {
	for _, k := range keys {
		v, ok := m[k]
		if !ok || v == nil {
			continue
		}
		switch n := v.(type) {
		case float64:
			return n
		case json.Number:
			f, _ := n.Float64()
			return f
		case string:
			var f float64
			if _, err := fmt.Sscan(strings.TrimSpace(n), &f); err == nil {
				return f
			}
		}
	}
	return 0
}

func linkDecodeFromSubscription(sub SubscriptionPreview) LinkDecodeResult {
	first := sub.Profiles[0]
	p := first
	res := LinkDecodeResult{
		Profile: &p,
	}
	if len(sub.Profiles) > 1 {
		res.Subscription = &sub
	}
	return res
}
