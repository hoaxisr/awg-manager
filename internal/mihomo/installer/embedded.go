package installer

const RequiredVersion = "1.19.29"

type BinarySpec struct {
	Version string
	URL     string
	SHA256  string
	Size    int64 // Uncompressed size in bytes
}

var EmbeddedBinaries = map[string]BinarySpec{
	// The legacy MIPS builds stay pinned to v1.18.7 until newer assets are validated
	// on real Linux 3.4 kernels. ARM64 uses v1.19.29 with sniffing support.
	"mipsel-3.4": {
		Version: "1.18.7",
		URL:     "https://github.com/MetaCubeX/mihomo/releases/download/v1.18.7/mihomo-linux-mipsle-softfloat-v1.18.7.gz",
		SHA256:  "8b504478c380f4ae7b5f98135cf9420bb074145eed1dda9781bc681d7fbb9ea9",
		Size:    30736550,
	},
	"mips-3.4": {
		Version: "1.18.7",
		URL:     "https://github.com/MetaCubeX/mihomo/releases/download/v1.18.7/mihomo-linux-mips-softfloat-v1.18.7.gz",
		SHA256:  "5a0ebc5c6c9c13a74c0e3db8233de90dfe6e01d1503451bfbf95aa79405c70ac",
		Size:    30736550,
	},
	"aarch64-3.10": {
		Version: RequiredVersion,
		URL:     "https://github.com/MetaCubeX/mihomo/releases/download/v1.19.29/mihomo-linux-arm64-v1.19.29.gz",
		SHA256:  "9a868b5e4e0ad91d9d71e1b41b0cfce78aaba44360c30df74a723f8e3926a86c",
		Size:    44236926,
	},
}
