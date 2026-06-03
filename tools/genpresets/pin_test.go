package main

import "testing"

func TestPinnedFetchURL(t *testing.T) {
	cases := map[string]string{
		"https://raw.githubusercontent.com/SagerNet/sing-geosite/rule-set/geosite-youtube.srs": "https://raw.githubusercontent.com/SagerNet/sing-geosite/" + sagerNetPinSHA + "/geosite-youtube.srs",
		"https://github.com/vernette/rulesets/raw/master/srs/youtube.srs":                      "https://github.com/vernette/rulesets/raw/" + vernettePinSHA + "/srs/youtube.srs",
		"https://example.com/whatever.srs":                                                     "https://example.com/whatever.srs",
	}
	for in, want := range cases {
		if got := pinnedFetchURL(in); got != want {
			t.Errorf("pinnedFetchURL(%q)\n  = %q\n want %q", in, got, want)
		}
	}
}
