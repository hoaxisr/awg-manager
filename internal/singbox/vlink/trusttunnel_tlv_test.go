package vlink

import (
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/base64"
	"math/big"
	"strings"
	"testing"
	"time"
)

const (
	ttOne    = "AQ92cG4uZXhhbXBsZS5jb20CCzEuMi4zLjQ6NDQzBQdwcmVtaXVtBgpzM2NyZXRQYXNzDAZCZXJsaW4"
	ttTwo    = "AAEBAQ92cG4uZXhhbXBsZS5jb20CCzEuMi4zLjQ6NDQzAhJbMjAwMTpkYjg6OjFdOjg0NDMDD2Nkbi5leGFtcGxlLm9yZwUHcHJlbWl1bQYKczNjcmV0UGFzcwcBAQkBAgoBAQsNYWFiYmNjL2ZmZmZmZgwFTXVsdGkqBmZ1dHVyZQ0IBzEuMS4xLjE"
	ttV2     = "AAECAQ92cG4uZXhhbXBsZS5jb20CCzEuMi4zLjQ6NDQzBQdwcmVtaXVtBgpzM2NyZXRQYXNzDAZCZXJsaW4"
	ttNoUser = "AQFoAgsxLjIuMy40OjQ0MwYBcA"
	ttTrunc  = "AAEBAQ92cG4uZXhh"
)

func TestDecodeTTPayload_Minimal(t *testing.T) {
	ep, err := decodeTTPayload(ttOne)
	if err != nil {
		t.Fatal(err)
	}
	if ep.Hostname != "vpn.example.com" || ep.Username != "premium" || ep.Password != "s3cretPass" || ep.Name != "Berlin" {
		t.Fatalf("fields: %+v", ep)
	}
	if len(ep.Addresses) != 1 || ep.Addresses[0] != "1.2.3.4:443" {
		t.Fatalf("addresses: %v", ep.Addresses)
	}
	if ep.SkipVerification || ep.AntiDPI || ep.CustomSNI != "" || ep.Certificate != "" {
		t.Fatalf("defaults broken: %+v", ep)
	}
}

func TestDecodeTTPayload_AllTagsAndUnknownSkipped(t *testing.T) {
	ep, err := decodeTTPayload(ttTwo)
	if err != nil {
		t.Fatal(err)
	}
	if len(ep.Addresses) != 2 || ep.Addresses[1] != "[2001:db8::1]:8443" {
		t.Fatalf("addresses: %v", ep.Addresses)
	}
	if ep.CustomSNI != "cdn.example.org" || !ep.SkipVerification || !ep.AntiDPI || ep.Name != "Multi" {
		t.Fatalf("fields: %+v", ep)
	}
}

func TestDecodeTTPayload_PaddedBase64Accepted(t *testing.T) {
	if _, err := decodeTTPayload(ttOne + "="); err != nil {
		t.Fatalf("padded: %v", err)
	}
}

func TestDecodeTTPayload_Errors(t *testing.T) {
	cases := map[string]string{
		"version 2":   ttV2,
		"no username": ttNoUser,
		"truncated":   ttTrunc,
		"empty":       "",
		"not base64":  "@@@",
	}
	for name, in := range cases {
		t.Run(name, func(t *testing.T) {
			if _, err := decodeTTPayload(in); err == nil {
				t.Fatal("expected error")
			}
		})
	}
	if _, err := decodeTTPayload(ttV2); err == nil || !strings.Contains(err.Error(), "версия") {
		t.Fatalf("version error must be explicit, got %v", err)
	}
}

func TestDecodeTTPayload_CertificateDERToPEM(t *testing.T) {
	// Самоподписанный сертификат, собранный в тесте, кладётся тегом 0x08 (DER).
	key, _ := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	tmpl := &x509.Certificate{SerialNumber: big.NewInt(1), Subject: pkix.Name{CommonName: "vpn.example.com"}, NotBefore: time.Now(), NotAfter: time.Now().Add(time.Hour)}
	der, err := x509.CreateCertificate(rand.Reader, tmpl, tmpl, &key.PublicKey, key)
	if err != nil {
		t.Fatal(err)
	}
	// TLV вручную: теги 1,2,5,6 из ttOne + тег 8 с DER (длина > 63 → 2-байтный varint).
	base, _ := base64.RawURLEncoding.DecodeString(ttOne)
	tlv := append([]byte{}, base...)
	tlv = append(tlv, 0x08, byte(0x40|len(der)>>8), byte(len(der)))
	tlv = append(tlv, der...)
	ep, err := decodeTTPayload(base64.RawURLEncoding.EncodeToString(tlv))
	if err != nil {
		t.Fatal(err)
	}
	if !strings.HasPrefix(ep.Certificate, "-----BEGIN CERTIFICATE-----") || !strings.HasSuffix(strings.TrimSpace(ep.Certificate), "-----END CERTIFICATE-----") {
		t.Fatalf("pem: %q", ep.Certificate)
	}
}

func TestReadVarint_MultiByte(t *testing.T) {
	// 0x40 0x80 = 2-байтный varint, значение 128
	v, n, err := readVarint([]byte{0x40, 0x80, 0xff})
	if err != nil || v != 128 || n != 2 {
		t.Fatalf("v=%d n=%d err=%v", v, n, err)
	}
	if _, _, err := readVarint([]byte{0x40}); err == nil {
		t.Fatal("short varint must fail")
	}
}
