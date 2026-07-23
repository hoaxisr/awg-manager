package wdtt

import (
	"encoding/json"
	"fmt"
	"strings"
)

// SubscriptionPreview is a multi-profile WDTT subscription document (_wdtt.json).
type SubscriptionPreview struct {
	Name           string          `json:"name"`
	Description    string          `json:"description,omitempty"`
	TrafficUsedMb  float64         `json:"trafficUsedMb,omitempty"`
	TrafficLimitMb float64         `json:"trafficLimitMb,omitempty"`
	UpdatedAt      string          `json:"updatedAt,omitempty"`
	SubURL         string          `json:"subUrl"`
	Profiles       []ImportPayload `json:"profiles"`
}

// LinkDecodeResult is returned by subscription-aware link decode.
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

func ProfilesFromDecode(res LinkDecodeResult) []ImportPayload {
	if res.Subscription != nil && len(res.Subscription.Profiles) > 0 {
		return res.Subscription.Profiles
	}
	if res.Profile != nil {
		return []ImportPayload{*res.Profile}
	}
	return nil
}

func FindProfileByPeer(profiles []ImportPayload, peer string) *ImportPayload {
	want := normalizePeer(peer)
	for _, p := range profiles {
		if normalizePeer(p.Peer) == want {
			copy := p
			return &copy
		}
	}
	return nil
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
