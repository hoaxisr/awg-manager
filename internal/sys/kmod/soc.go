package kmod

import (
	"regexp"
	"strings"

	"github.com/hoaxisr/awg-manager/internal/sys/ndmsinfo"
)

// SoC represents the router's System-on-Chip type.
type SoC string

const (
	SoCMT7621  SoC = "mt7621"
	SoCMT7628  SoC = "mt7628"
	SoCEN7512  SoC = "en7512"
	SoCEN7516  SoC = "en7516"
	SoCEN7528  SoC = "en7528"
	SoCMT7622  SoC = "mt7622"
	SoCMT7981  SoC = "mt7981"
	SoCMT7988  SoC = "mt7988"
	SoCUnknown SoC = ""
)

// Model to SoC mapping based on Keenetic hardware.
// KN-xxxx and NC-xxxx are equivalent (only digits matter).
var modelToSoC = map[string]SoC{
	// MT7621 (15 models)
	"1010": SoCMT7621,
	"1011": SoCMT7621,
	"1810": SoCMT7621,
	"1910": SoCMT7621,
	"1913": SoCMT7621,
	"2310": SoCMT7621,
	"2311": SoCMT7621,
	"2610": SoCMT7621,
	"2810": SoCMT7621,
	"2910": SoCMT7621,
	"2911": SoCMT7621,
	"3010": SoCMT7621,
	"3013": SoCMT7621,
	"3410": SoCMT7621,
	"3510": SoCMT7621,

	// MT7628 (33 models)
	"1110": SoCMT7628,
	"1111": SoCMT7628,
	"1112": SoCMT7628,
	"1121": SoCMT7628,
	"1210": SoCMT7628,
	"1211": SoCMT7628,
	"1212": SoCMT7628,
	"1213": SoCMT7628,
	"1214": SoCMT7628,
	"1215": SoCMT7628,
	"1216": SoCMT7628,
	"1217": SoCMT7628,
	"1218": SoCMT7628,
	"1219": SoCMT7628,
	"1220": SoCMT7628,
	"1221": SoCMT7628,
	"1310": SoCMT7628,
	"1311": SoCMT7628,
	"1410": SoCMT7628,
	"1510": SoCMT7628,
	"1511": SoCMT7628,
	"1610": SoCMT7628,
	"1611": SoCMT7628,
	"1612": SoCMT7628,
	"1613": SoCMT7628,
	"1614": SoCMT7628,
	"1615": SoCMT7628,
	"1616": SoCMT7628,
	"1617": SoCMT7628,
	"1618": SoCMT7628,
	"1619": SoCMT7628,
	"1620": SoCMT7628,
	"1621": SoCMT7628,
	"1710": SoCMT7628,
	"1711": SoCMT7628,
	"1712": SoCMT7628,
	"1713": SoCMT7628,
	"1714": SoCMT7628,
	"1715": SoCMT7628,
	"1716": SoCMT7628,
	"1717": SoCMT7628,
	"1718": SoCMT7628,
	"1719": SoCMT7628,
	"1720": SoCMT7628,
	"1721": SoCMT7628,
	"2210": SoCMT7628,
	"2211": SoCMT7628,
	"2212": SoCMT7628,
	"3210": SoCMT7628,
	"3211": SoCMT7628,
	"3212": SoCMT7628,
	"3310": SoCMT7628,
	"3311": SoCMT7628,
	"4910": SoCMT7628,

	// EN7512 (5 models)
	"2010": SoCEN7512,
	"2011": SoCEN7512,
	"2012": SoCEN7512,
	"2110": SoCEN7512,
	"2111": SoCEN7512,

	// EN7516 (4 models)
	"2112": SoCEN7516,
	"2410": SoCEN7516,
	"2510": SoCEN7516,
	"3610": SoCEN7516,

	// EN7528 (4 models)
	"1912": SoCEN7528,
	"3012": SoCEN7528,
	"3710": SoCEN7528,
	"3810": SoCEN7528,

	// MT7622 — AARCH64 (2 models)
	"1811": SoCMT7622, // Ultra / Titan
	"2710": SoCMT7622, // Peak

	// MT7981 — AARCH64 (11 models)
	"1012": SoCMT7981, // Giga / Hero
	"2312": SoCMT7981, // Hopper 4G+
	"3411": SoCMT7981, // Buddy 6
	"3711": SoCMT7981, // Sprinter
	"3712": SoCMT7981, // Sprinter SE
	"3811": SoCMT7981, // Hopper
	"3812": SoCMT7981, // Hopper SE
	"3910": SoCMT7981, // Challenger
	"3911": SoCMT7981, // Challenger SE
	"4010": SoCMT7981, // Racer
	"4410": SoCMT7981, // Buddy 6 SE

	// MT7988 — AARCH64 (1 model)
	"1812": SoCMT7988, // Ultra / Titan
}

// modelNumberRegex extracts digits from KN-xxxx or NC-xxxx format.
var modelNumberRegex = regexp.MustCompile(`(?:KN|NC)-(\d+)`)

// legacyHWIDToSoC maps old-style hw_id values (pre-KN era) to SoC types.
var legacyHWIDToSoC = map[string]SoC{
	"ki_rb":  SoCMT7628, // Keenetic Extra II
	"kng_re": SoCMT7621, // Keenetic Giga III (MT7621ST)
}

// DetectModel returns the router's hw_id from cached NDMS version info.
// Returns the hw_id as-is (e.g. "KN-1810", "ki_rb").
func DetectModel() string {
	info := ndmsinfo.Get()
	if info == nil || info.HwID == "" {
		return ""
	}
	return info.HwID
}

// DetectSoC returns the router's SoC from cached NDMS version info.
func DetectSoC() SoC {
	info := ndmsinfo.Get()
	if info == nil || info.HwID == "" {
		return SoCUnknown
	}
	return ParseModelToSoC(info.HwID)
}

// ParseModelToSoC converts a model string (KN-1810 or NC-1810) to SoC type.
func ParseModelToSoC(model string) SoC {
	// Check legacy hw_id first (e.g. "ki_rb")
	if soc, ok := legacyHWIDToSoC[model]; ok {
		return soc
	}

	matches := modelNumberRegex.FindStringSubmatch(strings.ToUpper(model))
	if len(matches) < 2 {
		return SoCUnknown
	}

	modelNum := matches[1]
	if soc, ok := modelToSoC[modelNum]; ok {
		return soc
	}

	return SoCUnknown
}

// ModulePath returns the path to the kernel module for this SoC.
func (s SoC) ModulePath() string {
	if s == SoCUnknown {
		return ""
	}
	return ModulesDir + "/" + string(s) + "/amneziawg.ko"
}

// IsMIPS returns true if this SoC uses big-endian MIPS.
func (s SoC) IsMIPS() bool {
	return s == SoCEN7512 || s == SoCEN7516
}

// IsMIPSEL returns true if this SoC uses little-endian MIPS.
func (s SoC) IsMIPSEL() bool {
	return s == SoCMT7621 || s == SoCMT7628 || s == SoCEN7528
}

// IsAARCH64 returns true if this SoC uses ARM64 architecture.
func (s SoC) IsAARCH64() bool {
	return s == SoCMT7622 || s == SoCMT7981 || s == SoCMT7988
}
