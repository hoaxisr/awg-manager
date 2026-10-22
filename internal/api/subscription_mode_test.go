package api

import (
	"testing"

	"github.com/hoaxisr/awg-manager/internal/singbox/subscription"
)

func TestParseModes_AcceptAutoSelectModes(t *testing.T) {
	for _, mode := range []string{"selector", "urltest", "fallback"} {
		if got, err := parseSubscriptionMode(mode); err != nil || string(got) != mode {
			t.Errorf("parseSubscriptionMode(%q) = %q, %v", mode, got, err)
		}
		if got, err := parseGroupMode(mode); err != nil || string(got) != mode {
			t.Errorf("parseGroupMode(%q) = %q, %v", mode, got, err)
		}
	}
	if _, err := parseSubscriptionMode("load-balance"); err == nil {
		t.Error("parseSubscriptionMode accepted load-balance")
	}
}

// URL и интервал проверки должны вернуться в UI во всех авто-режимах.
func TestSubscriptionMetaDTO_URLTestForAutoSelectModes(t *testing.T) {
	for _, mode := range []subscription.SubscriptionMode{subscription.ModeURLTest, subscription.ModeFallback} {
		dto := buildSubscriptionMetaDTO(subscription.Subscription{Mode: mode}, false)
		if dto.URLTest == nil {
			t.Errorf("%s: urlTest missing", mode)
		}
	}
	if dto := buildSubscriptionMetaDTO(subscription.Subscription{Mode: subscription.ModeSelector}, false); dto.URLTest != nil {
		t.Error("selector: urlTest must be omitted")
	}
}
