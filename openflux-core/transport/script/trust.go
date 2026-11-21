package script

import (
	"crypto/sha256"
	"encoding/hex"
	"strings"
)

// OfficialKeyHex is the OpenFlux project's own ed25519 signing key (hex). A
// script signed by it is shown as a first-party transport; every other key
// is an unknown author the app pins on trust (TOFU). It is a public key, so
// sharing the literal between the CLI and the gomobile bindings is fine -
// nothing secret crosses that boundary.
//
// This build (OpenFluxTeam) signs its transports with the team's key; the
// upstream project's key stays official (upstreamKeyHex), so what an app
// installed from upstream is still first-party and updates to ours.
const OfficialKeyHex = "96df2b44fb8e6b539f4407f299ea483542afcd26e7429e60fabb5ca39ab86b1a"

// upstreamKeyHex is p1neappleXpress/OpenFlux's signing key.
const upstreamKeyHex = "d8bf9c958b994c2faab886cade5f28213f254911f87abe5e34756a289ae91354"

// officialKeys are every key the core treats as the OpenFlux project's own.
// OfficialKeyHex (the first) is the one packages are signed with today. A
// rotation is two releases: the first adds the next key here (so every
// installed app learns to trust it), and only once that has been out for a
// while is the second one's signing key switched - an app that missed the
// first would reject the new signatures. An update of an official transport
// signed by another official key is accepted and re-pins the install to it
// (see UpdateReport.NewKey). A compromised key is retired by dropping it from
// this list in a release; installs pinned to it then stop being "official".
var officialKeys = []string{OfficialKeyHex, upstreamKeyHex}

// IsOfficialKey reports whether keyHex is one of the project's own keys.
func IsOfficialKey(keyHex string) bool {
	k := strings.ToLower(strings.ReplaceAll(strings.TrimSpace(keyHex), " ", ""))
	for _, o := range officialKeys {
		if k == strings.ToLower(o) {
			return true
		}
	}
	return false
}

// OfficialKeys returns the project's keys, the signing one first.
func OfficialKeys() []string { return append([]string(nil), officialKeys...) }

// TrustReport is what an app shows in its "trust this transport?" dialog
// before installing a downloaded script, and what it stores alongside the
// file afterwards (name, fingerprint, params). See InspectTrust.
type TrustReport struct {
	OK      bool    `json:"ok"`
	Error   string  `json:"error,omitempty"`
	Name    string  `json:"name,omitempty"`
	Version string  `json:"version,omitempty"`
	Params  []Param `json:"params,omitempty"`
	// ParamProblems are what is wrong with the declaration (Info.CheckParams):
	// for the author, never a reason to refuse the script.
	ParamProblems []string `json:"paramProblems,omitempty"`
	// SettingsPage: the script brings a settings page of its own, so it has
	// settings to open even with no setting params declared.
	SettingsPage  bool   `json:"settingsPage,omitempty"`
	Signature     string `json:"signature"` // "valid" | "invalid" | "unverified"
	Fingerprint   string `json:"fingerprint,omitempty"`
	Official      bool   `json:"official,omitempty"`
	Author        string `json:"author,omitempty"`
	PackageAuthor string `json:"packageAuthor,omitempty"`
	// From a .flux package's manifest; the app stores them with the install so
	// updates can be found and judged later (see CheckUpdate).
	ID     string   `json:"id,omitempty"`
	Wire   int      `json:"wire,omitempty"`
	API    int      `json:"api,omitempty"`
	Update []string `json:"update,omitempty"`
	// Auth is the sign-in the transport needs: the apps make an account of
	// it and give the transport only Auth.Cookies of that account.
	Auth *Auth `json:"auth,omitempty"`
	// Code is a machine reason when OK is false for a known cause
	// (CodeNeedsNewerApp).
	Code string `json:"code,omitempty"`
}

// InspectTrust reads a downloaded transport before it is trusted or run and
// reports what it claims to be and whether it is signed by pubkeyHex,
// WITHOUT ever running open() or touching the network. data is a .flux
// package (zip) or a bare .js source; sig is the detached signature for a
// bare .js (ignored for .flux, which carries its own); pubkeyHex is the
// candidate author key (from a share link, a GitHub release, or manual
// entry), or "" to inspect without a key. officialKeyHex, if non-empty, is
// compared against pubkeyHex to set Official/Author.
func InspectTrust(data, sig []byte, pubkeyHex, officialKeyHex string) TrustReport {
	report := TrustReport{Signature: "unverified"}

	pubkeyHex = strings.ReplaceAll(strings.TrimSpace(pubkeyHex), " ", "")
	var pub []byte
	if pubkeyHex != "" {
		p, err := DecodePublicKeyHex(pubkeyHex)
		if err != nil {
			report.Error = "ключ автора: " + err.Error()
			return report
		}
		pub = p
		sum := sha256.Sum256(pub)
		report.Fingerprint = hex.EncodeToString(sum[:])
		if (officialKeyHex != "" && strings.EqualFold(pubkeyHex, officialKeyHex)) || (officialKeyHex == OfficialKeyHex && IsOfficialKey(pubkeyHex)) {
			report.Official = true
			report.Author = "OpenFlux"
		}
	}

	var src []byte
	var manifestAuth *Auth
	if len(data) >= 2 && data[0] == 'P' && data[1] == 'K' { // .flux (zip)
		pkg, err := ReadPackage(data)
		if err != nil {
			report.Error = "пакет .flux: " + err.Error()
			return report
		}
		src = pkg.Script
		manifestAuth = pkg.Manifest.Auth
		report.ID, report.Wire, report.API, report.Update = pkg.Manifest.EffectiveID(), pkg.Manifest.EffectiveWire(), pkg.Manifest.EffectiveAPI(), pkg.Manifest.Update
		if report.API > APIVersion {
			report.Code = CodeNeedsNewerApp
			report.Error = "транспорту нужна более новая версия приложения"
			return report
		}
		if pkg.Manifest.Author != "" {
			report.PackageAuthor = pkg.Manifest.Author
		}
		if pub != nil {
			if pkg.Verify(pub) == nil {
				report.Signature = "valid"
			} else {
				report.Signature = "invalid"
			}
		}
	} else { // bare .js
		src = data
		if pub != nil && len(sig) > 0 {
			if VerifyScript(src, sig, pub) == nil {
				report.Signature = "valid"
			} else {
				report.Signature = "invalid"
			}
		}
	}

	info, err := Inspect(src)
	if err != nil {
		report.Error = "чтение манифеста: " + err.Error()
		return report
	}
	report.Name = info.Name
	report.Version = info.Version
	report.Params = info.ResolvedParams()
	report.ParamProblems = info.CheckParams()
	report.SettingsPage = info.CustomSettings
	if info.Auth == nil {
		info.Auth = manifestAuth
	}
	if err := info.Auth.Check(); err != nil {
		report.ParamProblems = append(report.ParamProblems, err.Error())
	} else {
		report.Auth = info.Auth
	}
	report.OK = true
	return report
}

// Fingerprint returns the SHA-256 (hex) of an author public key, the stable
// id an app shows the user to compare out of band. "" if the key can't be
// decoded.
func Fingerprint(pubkeyHex string) string {
	pub, err := DecodePublicKeyHex(strings.ReplaceAll(strings.TrimSpace(pubkeyHex), " ", ""))
	if err != nil {
		return ""
	}
	sum := sha256.Sum256(pub)
	return hex.EncodeToString(sum[:])
}
