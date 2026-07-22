package vkauth

import (
	"testing"

	"github.com/samosvalishe/free-turn-proxy/internal/provider/vk/internal/browserprofile"
)

func TestCaptchaFamiliesFor(t *testing.T) {
	t.Parallel()

	cases := []struct {
		base browserprofile.Kind
		want []browserprofile.Kind
	}{
		{browserprofile.Chrome, []browserprofile.Kind{browserprofile.Chrome, browserprofile.Firefox, browserprofile.Safari}},
		{browserprofile.Firefox, []browserprofile.Kind{browserprofile.Firefox, browserprofile.Chrome, browserprofile.Safari}},
		{browserprofile.Safari, []browserprofile.Kind{browserprofile.Safari, browserprofile.Firefox, browserprofile.Chrome}},
		{"", []browserprofile.Kind{browserprofile.Chrome, browserprofile.Firefox, browserprofile.Safari}},
	}
	for _, tc := range cases {
		got := captchaFamiliesFor(tc.base)
		if len(got) != len(tc.want) {
			t.Fatalf("base=%q: len=%d want=%d", tc.base, len(got), len(tc.want))
		}
		for i := range tc.want {
			if got[i] != tc.want[i] {
				t.Fatalf("base=%q: got[%d]=%q want=%q", tc.base, i, got[i], tc.want[i])
			}
		}
	}
}
