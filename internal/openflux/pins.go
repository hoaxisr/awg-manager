package openflux

import "strings"

// PinnedVersion is the exact version of OpenFlux core packaged for awg-manager.
const PinnedVersion = "0.17.1-awgm.1"

// BinarySpec describes an OpenFlux binary for a target architecture.
type BinarySpec struct {
	URL     string
	SHA256  string
	Version string
}

// EmbeddedBinaries lists release download specifications keyed by normalized router/server architecture.
var EmbeddedBinaries = map[string]BinarySpec{
	"aarch64-3.10": {
		URL:     "https://github.com/hoaxisr/awg-manager/releases/download/openflux-v" + PinnedVersion + "/openflux-linux-aarch64-3.10",
		SHA256:  "e6074cbea2ab987a030177823977f9ffdf473337110511748cbf15ad8e5897b5",
		Version: PinnedVersion,
	},
	"mipsel-3.4": {
		URL:     "https://github.com/hoaxisr/awg-manager/releases/download/openflux-v" + PinnedVersion + "/openflux-linux-mipsel-3.4",
		SHA256:  "8280cb9cb56b86183708e5bb0d388ce136a2cc35e86dc81240b6d0f8a35505cd",
		Version: PinnedVersion,
	},
	"mips-3.4": {
		URL:     "https://github.com/hoaxisr/awg-manager/releases/download/openflux-v" + PinnedVersion + "/openflux-linux-mips-3.4",
		SHA256:  "f838a8a4b854c0826c0e5b0f8c0d76e7397a629d40dad1c6d0ca56600cbc7cf6",
		Version: PinnedVersion,
	},
	"arm": {
		URL:     "https://github.com/hoaxisr/awg-manager/releases/download/openflux-v" + PinnedVersion + "/openflux-linux-arm",
		SHA256:  "f9a24d4815f1b38714f3abaaecee8f57f284875614f7c514d2bfe3cad7904053",
		Version: PinnedVersion,
	},
	"x86_64": {
		URL:     "https://github.com/hoaxisr/awg-manager/releases/download/openflux-v" + PinnedVersion + "/openflux-linux-x86_64",
		SHA256:  "861e39dc1751cec4e74ff3b3546a38008d3f65db608c7bb04acc09f51ac636d7",
		Version: PinnedVersion,
	},
}

// NormalizeArch converts raw system architecture names into EmbeddedBinaries keys.
func NormalizeArch(arch string) string {
	a := strings.ToLower(strings.TrimSpace(arch))
	switch a {
	case "aarch64", "arm64", "aarch64-3.10":
		return "aarch64-3.10"
	case "mipsel", "mipsle", "mipsel-3.4":
		return "mipsel-3.4"
	case "mips", "mips-3.4":
		return "mips-3.4"
	case "arm", "armv7", "armv7l", "armhf":
		return "arm"
	case "x86_64", "amd64", "x64":
		return "x86_64"
	default:
		return a
	}
}

// IsArchSupported reports whether an OpenFlux binary spec exists for arch.
func IsArchSupported(arch string) bool {
	_, ok := EmbeddedBinaries[NormalizeArch(arch)]
	return ok
}
