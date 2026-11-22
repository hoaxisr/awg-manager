package telemt

import "strings"

const (
	// PinnedTelemtVersion is the validated stable upstream release of telemt.
	PinnedTelemtVersion = "3.5.7"

	// Ownership: config.toml, raw.toml and /opt/var/run/telemt{,-raw}.pid in
	// the shared telemt directory belong to the Telegram WebProxy workers
	// (internal/tgwebproxy, init scripts S99telemt/S96telemt-raw). The
	// standalone service only uses the files below. Its pid file must also stay
	// outside tgwebproxy's factory-reset glob /opt/var/run/telemt*.pid*.
	ManagedTelemtDir        = "/opt/etc/awg-manager/telemt"
	ManagedTelemtBinaryPath = "/opt/etc/awg-manager/telemt/telemt"
	ManagedTelemtConfigPath = "/opt/etc/awg-manager/telemt/" + StandaloneConfigName
	ManagedTelemtPIDPath    = "/opt/var/run/awgm-telemt-standalone.pid"
	LegacyTelemtBinaryPath  = "/opt/bin/telemt"

	StandaloneConfigName = "standalone.toml"
	StandalonePIDName    = "standalone.pid"

	DefaultPort      = 8443
	DefaultListenIP  = "0.0.0.0"
	DefaultTLSDomain = "yandex.ru"
)

// BinarySpec defines download metadata for an architecture.
type BinarySpec struct {
	Version     string
	URL         string
	SHA256      string // SHA256 of the tar.gz archive
	ArchiveSize int64
	BinarySize  int64
}

// EmbeddedBinaries holds download specifications for supported router architectures.
// Upstream telemt is built in Rust musl; official prebuilts only exist for aarch64 and x86_64.
// MIPS/MIPSEL are intentionally omitted because upstream does not provide softfloat builds.
var EmbeddedBinaries = map[string]BinarySpec{
	"aarch64": {
		Version:     PinnedTelemtVersion,
		URL:         "https://github.com/telemt/telemt/releases/download/3.5.7/telemt-aarch64-linux-musl.tar.gz",
		SHA256:      "8730080863f8f8ed52ee11f9c51c4daa30b3044fc842538bf6dd8c91ac0572c1",
		ArchiveSize: 6059751,
		BinarySize:  16495864,
	},
	"x86_64": {
		Version:     PinnedTelemtVersion,
		URL:         "https://github.com/telemt/telemt/releases/download/3.5.7/telemt-x86_64-linux-musl.tar.gz",
		SHA256:      "db26e363bb98f11a02a7fd6d0df455f4987af5cdb2a5897da7f6fb8d613fbf41",
		ArchiveSize: 6626014,
		BinarySize:  16471288,
	},
}

// NormalizeArch standardizes router architecture strings.
func NormalizeArch(arch string) string {
	a := strings.ToLower(strings.TrimSpace(arch))
	switch {
	case strings.HasPrefix(a, "aarch64") || strings.HasPrefix(a, "arm64"):
		return "aarch64"
	case strings.HasPrefix(a, "mipsel") || strings.HasPrefix(a, "mipsle"):
		return "mipsel"
	case strings.HasPrefix(a, "mips"):
		return "mips"
	case strings.HasPrefix(a, "x86_64") || strings.HasPrefix(a, "amd64"):
		return "x86_64"
	default:
		return a
	}
}

// IsArchSupported returns true if the normalized architecture has a supported prebuilt binary.
func IsArchSupported(arch string) bool {
	norm := NormalizeArch(arch)
	_, ok := EmbeddedBinaries[norm]
	return ok
}
