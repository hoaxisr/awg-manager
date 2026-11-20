package firc

import (
	"encoding/json"
	"reflect"
	"testing"

	"gopkg.in/yaml.v3"
)

func TestEndpointDNSRoundTrip(t *testing.T) {
	for _, servers := range [][]string{nil, {}, {"9.9.9.9", "[2606:4700:4700::1111]:53"}} {
		original := Tunnel{ID: "dns", EndpointDNS: servers}
		for _, format := range []string{"json", "yaml"} {
			var got Tunnel
			var err error
			if format == "json" {
				var data []byte
				data, err = json.Marshal(original)
				if err == nil {
					err = json.Unmarshal(data, &got)
				}
			} else {
				var data []byte
				data, err = yaml.Marshal(original)
				if err == nil {
					err = yaml.Unmarshal(data, &got)
				}
			}
			if err != nil || !reflect.DeepEqual(servers, got.EndpointDNS) {
				t.Fatalf("%s: want %#v got %#v: %v", format, servers, got.EndpointDNS, err)
			}
		}
	}
}
