module github.com/hoaxisr/awg-manager

go 1.25.0

require github.com/coder/websocket v1.8.14

require (
	github.com/9seconds/mtg/v2 v2.2.8
	github.com/enfein/mieru/v3 v3.36.0
	google.golang.org/protobuf v1.36.11
	gopkg.in/yaml.v3 v3.0.1
)

replace github.com/9seconds/mtg/v2 => github.com/AYastrebov/mtg/v2 v2.0.0-20261017111415-962efeccda0c

require (
	github.com/BurntSushi/toml v1.6.0
	github.com/hoaxisr/awg-manager/awgmproto v0.1.2
	github.com/modelcontextprotocol/go-sdk v1.7.0
	github.com/refraction-networking/utls v1.8.2
	github.com/xtls/reality v0.0.0-20260322125925-9234c772ba8f
	go.uber.org/goleak v1.3.0
	golang.org/x/crypto v0.52.0
	golang.org/x/net v0.55.0
	golang.org/x/sys v0.45.0
)

require (
	github.com/OneOfOne/xxhash v1.2.8 // indirect
	github.com/andybalholm/brotli v1.0.6 // indirect
	github.com/cloudflare/circl v1.6.3 // indirect
	github.com/google/jsonschema-go v0.4.3 // indirect
	github.com/juju/ratelimit v1.0.2 // indirect
	github.com/klauspost/compress v1.18.3 // indirect
	github.com/mattn/go-colorable v0.1.14 // indirect
	github.com/mattn/go-isatty v0.0.20 // indirect
	github.com/panjf2000/ants/v2 v2.12.0 // indirect
	github.com/pires/go-proxyproto v0.11.0 // indirect
	github.com/rs/zerolog v1.35.0 // indirect
	github.com/segmentio/asm v1.1.3 // indirect
	github.com/segmentio/encoding v0.5.4 // indirect
	github.com/tylertreat/BoomFilters v0.0.0-20251117164519-53813c36cc1b // indirect
	github.com/yl2chen/cidranger v1.0.2 // indirect
	github.com/yosida95/uritemplate/v3 v3.0.2 // indirect
	golang.org/x/oauth2 v0.35.0 // indirect
	golang.org/x/sync v0.20.0 // indirect
	golang.org/x/text v0.37.0 // indirect
	golang.org/x/time v0.15.0 // indirect
)
