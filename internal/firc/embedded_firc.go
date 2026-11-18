package firc

import (
	_ "embed"
	"encoding/hex"
	"encoding/json"
	"net/url"
)

const FircRequiredVersion = "unreleased"

func validHTTPSURL(rawURL string) bool {
	u, err := url.Parse(rawURL)
	return err == nil && u.Scheme == "https" && u.Host != "" && u.User == nil
}

// IsAvailable reports whether the spec has valid download URLs and checksums.
func (s FircBinarySpec) IsAvailable() bool {
	digest, hexErr := hex.DecodeString(s.PackageSHA256)
	mirrorOK := s.MirrorURL == "" || validHTTPSURL(s.MirrorURL)
	return validRuntimeVersion(s.Version) &&
		s.PackageSize > 0 &&
		s.PackageSize <= maxRuntimeIPKBytes &&
		validHTTPSURL(s.PackageURL) &&
		mirrorOK &&
		hexErr == nil &&
		len(digest) == 32
}

// EmbeddedFircBinaries maps architecture to FIRC binary download specs.
// URLs and SHA256 digests will be populated when FIRC releases are published.
//
//go:embed firc_releases.json
var runtimeReleasesJSON []byte

var EmbeddedFircBinaries = func() map[string]FircBinarySpec {
	var specs map[string]FircBinarySpec
	if err := json.Unmarshal(runtimeReleasesJSON, &specs); err != nil {
		panic("invalid embedded FIRC release manifest: " + err.Error())
	}
	return specs
}()
