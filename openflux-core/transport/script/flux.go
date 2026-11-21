package script

import (
	"archive/zip"
	"bytes"
	"crypto/ed25519"
	"encoding/binary"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"strings"
)

// PackageManifest is the distribution-level metadata a .flux package carries
// - author/description/icon for a UI listing of available transports. It is
// entirely separate from Info (the RUNTIME manifest a script returns from
// its own Transport.info()): this one is read without ever booting a goja
// Runtime, and the signature covers it together with the script and icon
// bytes, so a legitimate transport's name/author/description can't be
// swapped without invalidating the signature.
type PackageManifest struct {
	// ID is the transport's stable identity across versions and mirrors: what
	// an update index must name and what the app files the package under. Empty
	// in older packages; EffectiveID then derives it from Name.
	ID          string `json:"id,omitempty"`
	Name        string `json:"name"`
	Version     string `json:"version"`
	Author      string `json:"author"`
	Description string `json:"description"`
	Icon        string `json:"icon,omitempty"` // filename inside the archive, e.g. "icon.png"

	// Wire is the generation of the transport's own wire format. A client and
	// a node both run the transport, so two versions with the same Wire always
	// interoperate and an update between them is safe; a different Wire is a
	// breaking change both ends must take together. 0 (absent) means 1.
	Wire int `json:"wire,omitempty"`
	// API is the host-API generation (APIVersion) the script was written for;
	// a core that knows only an older one refuses it. 0 (absent) means 1.
	API int `json:"api,omitempty"`
	// Update lists the https URLs of the author's update.json (the first
	// that answers wins; later ones are mirrors). Covered by the signature
	// like everything else here, so an update source cannot be swapped for a
	// package without re-signing it.
	Update []string `json:"update,omitempty"`
	// Auth repeats the script's info().auth so a listing can show the
	// sign-in a transport needs without running it.
	Auth *Auth `json:"auth,omitempty"`
}

// APIVersion is the host-API generation this core implements. A package
// declaring a newer one is refused instead of half-running.
const APIVersion = 1

// EffectiveID is the id the package is filed and updated under.
func (m PackageManifest) EffectiveID() string {
	if id := strings.TrimSpace(m.ID); id != "" {
		return id
	}
	return strings.ToLower(strings.ReplaceAll(strings.TrimSpace(m.Name), " ", "-"))
}

// EffectiveWire is Wire with the "absent means 1" rule applied.
func (m PackageManifest) EffectiveWire() int {
	if m.Wire <= 0 {
		return 1
	}
	return m.Wire
}

// EffectiveAPI is API with the "absent means 1" rule applied.
func (m PackageManifest) EffectiveAPI() int {
	if m.API <= 0 {
		return 1
	}
	return m.API
}

// Package is one loaded, signature-verified .flux file.
type Package struct {
	Manifest PackageManifest
	Script   []byte
	Icon     []byte // Manifest.Icon's raw bytes; nil if Manifest.Icon == ""
}

const (
	packageManifestEntry = "manifest.json"
	packageScriptEntry   = "main.js"
	packageSigEntry      = "package.sig"
)

// PackagePayload builds the exact byte sequence a .flux package's signature
// covers: length-prefixed manifest JSON + script + icon, in that fixed
// order. Framing (rather than plain concatenation) means an empty/missing
// icon can never be confused with a shifted boundary between fields -
// cmd/scriptsign's pack command and LoadSignedPackage both call this, so
// signing and verification can never disagree about what the signature
// actually covers.
func PackagePayload(manifestJSON, scriptSrc, icon []byte) []byte {
	buf := make([]byte, 0, 12+len(manifestJSON)+len(scriptSrc)+len(icon))
	buf = appendLenPrefixed(buf, manifestJSON)
	buf = appendLenPrefixed(buf, scriptSrc)
	buf = appendLenPrefixed(buf, icon)
	return buf
}

func appendLenPrefixed(buf, b []byte) []byte {
	var length [4]byte
	binary.BigEndian.PutUint32(length[:], uint32(len(b)))
	buf = append(buf, length[:]...)
	return append(buf, b...)
}

// LoadSignedPackage opens a .flux archive at path and verifies its signature
// against pubKey before returning the verified Package. Fails closed exactly
// like LoadSigned: there is no partially-trusted path, and a package whose
// manifest.json/main.js/icon don't match what was signed is rejected
// outright, not loaded with a warning.
func LoadSignedPackage(path string, pubKey ed25519.PublicKey) (*Package, error) {
	r, err := zip.OpenReader(path)
	if err != nil {
		return nil, fmt.Errorf("script: open %s: %w", path, err)
	}
	defer r.Close()

	if err := checkPackageEntries(len(r.File)); err != nil {
		return nil, fmt.Errorf("script: %s: %w", path, err)
	}
	files := map[string][]byte{}
	for _, f := range r.File {
		b, err := readZipFile(f)
		if err != nil {
			return nil, fmt.Errorf("script: %s: read %s: %w", path, f.Name, err)
		}
		files[f.Name] = b
	}

	manifestJSON, ok := files[packageManifestEntry]
	if !ok {
		return nil, fmt.Errorf("script: %s: missing %s", path, packageManifestEntry)
	}
	scriptSrc, ok := files[packageScriptEntry]
	if !ok {
		return nil, fmt.Errorf("script: %s: missing %s", path, packageScriptEntry)
	}
	sig, ok := files[packageSigEntry]
	if !ok {
		return nil, fmt.Errorf("script: %s: missing %s", path, packageSigEntry)
	}

	var manifest PackageManifest
	if err := json.Unmarshal(manifestJSON, &manifest); err != nil {
		return nil, fmt.Errorf("script: %s: decode %s: %w", path, packageManifestEntry, err)
	}
	if manifest.Name == "" {
		return nil, fmt.Errorf("script: %s: %s must set \"name\"", path, packageManifestEntry)
	}

	var icon []byte
	if manifest.Icon != "" {
		icon, ok = files[manifest.Icon]
		if !ok {
			return nil, fmt.Errorf("script: %s: manifest references icon %q, not found in archive", path, manifest.Icon)
		}
	}

	payload := PackagePayload(manifestJSON, scriptSrc, icon)
	if err := VerifyScript(payload, sig, pubKey); err != nil {
		return nil, fmt.Errorf("script: %s: %w", path, err)
	}

	return &Package{Manifest: manifest, Script: scriptSrc, Icon: icon}, nil
}

// RawPackage is a .flux archive read WITHOUT verifying its signature, for
// inspection before any key is trusted. It keeps the raw manifest JSON and
// signature so Verify can recompute the exact signed payload later, once the
// caller has a candidate key to check against (TOFU: the key arrives with the
// share link, not inside the package).
type RawPackage struct {
	Manifest     PackageManifest
	ManifestJSON []byte
	Script       []byte
	Icon         []byte
	Sig          []byte
}

// ReadPackage reads a .flux archive from memory without checking its
// signature. Use Inspect(pkg.Script) to read the runtime manifest, and
// Verify to check the signature once a key is chosen.
func ReadPackage(data []byte) (*RawPackage, error) {
	zr, err := zip.NewReader(bytes.NewReader(data), int64(len(data)))
	if err != nil {
		return nil, fmt.Errorf("script: open .flux: %w", err)
	}
	if err := checkPackageEntries(len(zr.File)); err != nil {
		return nil, fmt.Errorf("script: %w", err)
	}
	files := map[string][]byte{}
	for _, f := range zr.File {
		b, err := readZipFile(f)
		if err != nil {
			return nil, fmt.Errorf("script: read %s: %w", f.Name, err)
		}
		files[f.Name] = b
	}
	manifestJSON, ok := files[packageManifestEntry]
	if !ok {
		return nil, fmt.Errorf("script: missing %s", packageManifestEntry)
	}
	scriptSrc, ok := files[packageScriptEntry]
	if !ok {
		return nil, fmt.Errorf("script: missing %s", packageScriptEntry)
	}
	var manifest PackageManifest
	if err := json.Unmarshal(manifestJSON, &manifest); err != nil {
		return nil, fmt.Errorf("script: decode %s: %w", packageManifestEntry, err)
	}
	var icon []byte
	if manifest.Icon != "" {
		icon = files[manifest.Icon]
	}
	return &RawPackage{
		Manifest:     manifest,
		ManifestJSON: manifestJSON,
		Script:       scriptSrc,
		Icon:         icon,
		Sig:          files[packageSigEntry],
	}, nil
}

// Verify checks the package's signature against pubKey, recomputing the same
// length-prefixed payload LoadSignedPackage and WritePackage use.
func (p *RawPackage) Verify(pubKey ed25519.PublicKey) error {
	return VerifyScript(PackagePayload(p.ManifestJSON, p.Script, p.Icon), p.Sig, pubKey)
}

// Limits on a .flux archive. It is read BEFORE its signature is checked (an
// import dialog, an update download), so what it claims to contain must not be
// able to exhaust memory: a few KB of deflate can inflate to gigabytes.
const (
	maxPackageEntries   = 16
	maxPackageEntryByte = 8 << 20
)

func readZipFile(f *zip.File) ([]byte, error) {
	if f.UncompressedSize64 > maxPackageEntryByte {
		return nil, fmt.Errorf("entry larger than %d bytes", maxPackageEntryByte)
	}
	rc, err := f.Open()
	if err != nil {
		return nil, err
	}
	defer rc.Close()
	b, err := io.ReadAll(io.LimitReader(rc, maxPackageEntryByte+1))
	if err != nil {
		return nil, err
	}
	if len(b) > maxPackageEntryByte { // the header lied about the size
		return nil, fmt.Errorf("entry larger than %d bytes", maxPackageEntryByte)
	}
	return b, nil
}

func checkPackageEntries(n int) error {
	if n > maxPackageEntries {
		return fmt.Errorf("package has %d entries, at most %d are allowed", n, maxPackageEntries)
	}
	return nil
}

// WritePackage builds and signs a .flux archive at path. compress picks
// Deflate (true) or Store/uncompressed (false) for every entry - a package
// can ship either way, the reader doesn't care which. This is the one place
// that knows the archive's exact layout, shared by cmd/scriptsign's pack
// command and anything else that needs to build a package (tests, a future
// in-app packager), so authoring and the framing in PackagePayload can never
// drift apart.
func WritePackage(path string, manifest PackageManifest, scriptSrc, icon []byte, priv ed25519.PrivateKey, compress bool) error {
	manifestJSON, err := json.MarshalIndent(manifest, "", "  ")
	if err != nil {
		return fmt.Errorf("script: encode manifest: %w", err)
	}
	payload := PackagePayload(manifestJSON, scriptSrc, icon)
	sig := ed25519.Sign(priv, payload)

	method := uint16(zip.Deflate)
	if !compress {
		method = zip.Store
	}

	f, err := os.Create(path)
	if err != nil {
		return fmt.Errorf("script: create %s: %w", path, err)
	}
	defer f.Close()

	zw := zip.NewWriter(f)
	if err := writeZipEntry(zw, packageManifestEntry, manifestJSON, method); err != nil {
		return err
	}
	if err := writeZipEntry(zw, packageScriptEntry, scriptSrc, method); err != nil {
		return err
	}
	if manifest.Icon != "" {
		if err := writeZipEntry(zw, manifest.Icon, icon, method); err != nil {
			return err
		}
	}
	if err := writeZipEntry(zw, packageSigEntry, sig, method); err != nil {
		return err
	}
	return zw.Close()
}

func writeZipEntry(zw *zip.Writer, name string, data []byte, method uint16) error {
	w, err := zw.CreateHeader(&zip.FileHeader{Name: name, Method: method})
	if err != nil {
		return fmt.Errorf("script: zip entry %s: %w", name, err)
	}
	_, err = w.Write(data)
	return err
}
