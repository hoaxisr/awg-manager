package openflux

import "strings"

// PinnedVersion is the exact version of OpenFlux core packaged for awg-manager.
const PinnedVersion = "0.17.1-awgm.2"

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
		SHA256:  "da8eeffea12a8af721fc77542160d0910561c5b93e642afac5525eb95d318813",
		Version: PinnedVersion,
	},
	"mipsel-3.4": {
		URL:     "https://github.com/hoaxisr/awg-manager/releases/download/openflux-v" + PinnedVersion + "/openflux-linux-mipsel-3.4",
		SHA256:  "cb221d8d85b8363d7b3e5c322dcd468c91d2b58c4ea4c089af53f090cdfbdd63",
		Version: PinnedVersion,
	},
	"mips-3.4": {
		URL:     "https://github.com/hoaxisr/awg-manager/releases/download/openflux-v" + PinnedVersion + "/openflux-linux-mips-3.4",
		SHA256:  "5bc37a0a2731ea8c98161074ac8f39046e2f9edef3c1a6fb1bede88fbf6c1283",
		Version: PinnedVersion,
	},
	"arm": {
		URL:     "https://github.com/hoaxisr/awg-manager/releases/download/openflux-v" + PinnedVersion + "/openflux-linux-arm",
		SHA256:  "2dd9be872a1a0d84d67e0358fc51b2730e2228b04c723720a408ed8c7c88556c",
		Version: PinnedVersion,
	},
	"x86_64": {
		URL:     "https://github.com/hoaxisr/awg-manager/releases/download/openflux-v" + PinnedVersion + "/openflux-linux-x86_64",
		SHA256:  "570ecb2d3c96514973712b8543554eb903c5829303438aa83dbc0bdbbce8057f",
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
