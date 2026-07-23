package wdtt

import "testing"

const sampleSubscriptionJSON = `{
  "subscriptionName": "DarkBit VPN",
  "description": "Подписка · до 22.08.2026",
  "trafficUsedMb": 0,
  "trafficLimitMb": 102400,
  "updatedAt": "2026-07-23",
  "version": 1,
  "profiles": [
    {
      "name": "Германия",
      "peer": "144.31.223.246:56000",
      "password": "secret",
      "hashes": "hash1",
      "workersPerHash": 16,
      "listenPort": 9000
    },
    {
      "name": "Финляндия",
      "peer": "31.76.102.29:56000",
      "password": "secret",
      "hashes": "hash1",
      "workersPerHash": 16,
      "listenPort": 9000
    }
  ]
}`

func TestDecodeLink_SubscriptionJSON(t *testing.T) {
	got, err := DecodeLink(sampleSubscriptionJSON)
	if err != nil {
		t.Fatal(err)
	}
	if got.Subscription == nil {
		t.Fatal("expected subscription preview")
	}
	if got.Subscription.Name != "DarkBit VPN" {
		t.Fatalf("name=%q", got.Subscription.Name)
	}
	if len(got.Subscription.Profiles) != 2 {
		t.Fatalf("profiles=%d", len(got.Subscription.Profiles))
	}
	if got.Subscription.Profiles[0].Peer != "144.31.223.246:56000" {
		t.Fatalf("peer0=%q", got.Subscription.Profiles[0].Peer)
	}
	if got.Subscription.Profiles[0].Workers != 16 {
		t.Fatalf("workers0=%d", got.Subscription.Profiles[0].Workers)
	}
}

func TestDecodeLink_SubscriptionURL(t *testing.T) {
	// Offline: same body as HTTPS subscription would return.
	got, err := DecodeLink(sampleSubscriptionJSON)
	if err != nil {
		t.Fatal(err)
	}
	if got.Profile == nil || got.Profile.Peer == "" {
		t.Fatalf("profile=%+v", got.Profile)
	}
}
