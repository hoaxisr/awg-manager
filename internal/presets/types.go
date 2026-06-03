package presets

// Origin marks where a preset came from. It is always computed on read
// (in Merge); the value in the on-disk overlay file is never trusted.
type Origin string

const (
	OriginBuiltin Origin = "builtin"
	OriginUser    Origin = "user"
)

// Preset is a reusable service template. It is NOT an applied route — applied
// routes are separate instances that reference a preset by ID.
type Preset struct {
	ID        string  `json:"id"`
	Name      string  `json:"name"`
	IconSlug  string  `json:"iconSlug"`
	Category  string  `json:"category"` // social|media|ai|developer|cloud|gaming|block (free string)
	Notice    string  `json:"notice,omitempty"`
	Featured  bool    `json:"featured,omitempty"`
	Sensitive bool    `json:"sensitive,omitempty"`
	Origin    Origin  `json:"origin"`
	Engines   Engines `json:"engines"`
}

// Engines holds per-engine payloads. A nil pointer means the preset does not
// support that engine (this is how the hybrid model is expressed).
type Engines struct {
	DNS        *DNSEngine        `json:"dns,omitempty"`
	Singbox    *SingboxEngine    `json:"singbox,omitempty"`
	HydraRoute *HydraRouteEngine `json:"hydraroute,omitempty"`
}

// DNSEngine is self-sufficient without sing-box: inline domains/subnets, or a
// remote subscriptionUrl. It is never derived at runtime via sing-box.
type DNSEngine struct {
	Domains         []string `json:"domains,omitempty"`
	Subnets         []string `json:"subnets,omitempty"`
	SubscriptionURL string   `json:"subscriptionUrl,omitempty"`
}

type RuleRef struct {
	Tag string `json:"tag"`
	URL string `json:"url"`
}

type SingboxEngine struct {
	RuleSets []RuleRef `json:"ruleSets,omitempty"`
	Action   string    `json:"action"` // tunnel|reject|direct
}

type HydraRouteEngine struct {
	GeoTags []string `json:"geoTags,omitempty"`
}
