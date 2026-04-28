// internal/ndms/query/policy_marks.go
package query

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
)

// ErrPolicyMarkNotFound is returned by PolicyMarkStore.Get when the
// requested policy is absent from /show/ip/policy or its mark is empty.
var ErrPolicyMarkNotFound = errors.New("policy mark not found")

// PolicyMarkStore fetches NDMS-assigned fwmarks for access policies
// from the runtime endpoint /show/ip/policy. Distinct from PolicyStore
// (which reads /show/rc/ip/policy — the config view, no marks).
//
// JSON shape (verified on hardware, NDMS 4.x):
//
//	{
//	  "Policy0": {"description":"IoT_VPN","mark":"ffffaaa","table4":4096,...},
//	  "Policy1": {"description":"Only_Letai","mark":"ffffaab","table4":4098,...}
//	}
//
// Top-level map (no "policy" wrapper); mark is bare hex without "0x"
// prefix; we add the prefix when returning so iptables --mark accepts
// it directly.
//
// No caching: marks are read on demand because they're consumed
// rarely (router Enable + Reconcile mark-change check) and stale marks
// would silently route via the wrong tunnel.
type PolicyMarkStore struct {
	getter Getter
	log    Logger
}

func NewPolicyMarkStore(g Getter, log Logger) *PolicyMarkStore {
	return &PolicyMarkStore{getter: g, log: log}
}

type policyMarkWire struct {
	Mark string `json:"mark"`
}

// Get returns the hex-formatted mark (e.g. "0xffffaaa") for policyName.
// Returns ErrPolicyMarkNotFound if the policy is absent or its mark is empty.
func (s *PolicyMarkStore) Get(ctx context.Context, policyName string) (string, error) {
	body, err := s.getter.GetRaw(ctx, "/show/ip/policy")
	if err != nil {
		return "", fmt.Errorf("fetch policy marks: %w", err)
	}
	var doc map[string]policyMarkWire
	if err := json.Unmarshal(body, &doc); err != nil {
		return "", fmt.Errorf("decode policy marks: %w", err)
	}
	p, ok := doc[policyName]
	if !ok || p.Mark == "" {
		return "", ErrPolicyMarkNotFound
	}
	return "0x" + p.Mark, nil
}
