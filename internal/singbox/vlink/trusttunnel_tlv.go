package vlink

import (
	"bytes"
	"crypto/x509"
	"encoding/pem"
	"errors"
	"fmt"
)

// Deep-link TrustTunnel (DEEP_LINK.md, версия формата 1): base64url(TLV),
// tag и length — varint RFC 9000, неизвестные теги пропускаются.
const ttMaxVersion = 1

const (
	ttTagVersion     = 0x00
	ttTagHostname    = 0x01
	ttTagAddresses   = 0x02
	ttTagCustomSNI   = 0x03
	ttTagUsername    = 0x05
	ttTagPassword    = 0x06
	ttTagSkipVerify  = 0x07
	ttTagCertificate = 0x08
	ttTagAntiDPI     = 0x0A
	ttTagName        = 0x0C
	// 0x04 has_ipv6, 0x0B client_random_prefix, 0x0D dns_upstreams — не читаем (спека, Q3/Q9).
	// 0x09 upstream_protocol не читаем: H2-only, quic всегда false
)

type ttEndpoint struct {
	Hostname         string
	Addresses        []string
	CustomSNI        string
	Username         string
	Password         string
	SkipVerification bool
	Certificate      string // PEM-цепочка или ""
	AntiDPI          bool
	Name             string
}

func decodeTTPayload(b64 string) (ttEndpoint, error) {
	if b64 == "" {
		return ttEndpoint{}, errors.New("trusttunnel: пустой payload")
	}
	raw, err := DecodeBase64Url(b64)
	if err != nil {
		return ttEndpoint{}, fmt.Errorf("trusttunnel: base64url: %w", err)
	}
	return parseTTTLV(raw)
}

// readVarint читает varint RFC 9000 из начала b: значение, число байт.
func readVarint(b []byte) (uint64, int, error) {
	if len(b) == 0 {
		return 0, 0, errors.New("trusttunnel: обрыв TLV")
	}
	n := 1 << (b[0] >> 6)
	if len(b) < n {
		return 0, 0, errors.New("trusttunnel: обрыв varint")
	}
	v := uint64(b[0] & 0x3f)
	for i := 1; i < n; i++ {
		v = v<<8 | uint64(b[i])
	}
	return v, n, nil
}

func parseTTTLV(data []byte) (ttEndpoint, error) {
	var ep ttEndpoint
	for len(data) > 0 {
		tag, n, err := readVarint(data)
		if err != nil {
			return ttEndpoint{}, err
		}
		data = data[n:]
		length, n, err := readVarint(data)
		if err != nil {
			return ttEndpoint{}, err
		}
		data = data[n:]
		if uint64(len(data)) < length {
			return ttEndpoint{}, errors.New("trusttunnel: обрыв значения TLV")
		}
		val := data[:length]
		data = data[length:]
		switch tag {
		case ttTagVersion:
			v, _, err := readVarint(val)
			if err != nil {
				return ttEndpoint{}, err
			}
			if v > ttMaxVersion {
				return ttEndpoint{}, fmt.Errorf("trusttunnel: версия deep-link %d не поддерживается (максимум %d)", v, ttMaxVersion)
			}
		case ttTagHostname:
			ep.Hostname = string(val)
		case ttTagAddresses:
			ep.Addresses = append(ep.Addresses, string(val))
		case ttTagCustomSNI:
			ep.CustomSNI = string(val)
		case ttTagUsername:
			ep.Username = string(val)
		case ttTagPassword:
			ep.Password = string(val)
		case ttTagSkipVerify:
			ep.SkipVerification = len(val) == 1 && val[0] == 1
		case ttTagCertificate:
			pemChain, err := derChainToPEM(val)
			if err != nil {
				return ttEndpoint{}, err
			}
			ep.Certificate = pemChain
		case ttTagAntiDPI:
			ep.AntiDPI = len(val) == 1 && val[0] == 1
		case ttTagName:
			ep.Name = string(val)
		}
	}
	switch {
	case ep.Hostname == "":
		return ttEndpoint{}, errors.New("trusttunnel: нет hostname")
	case len(ep.Addresses) == 0:
		return ttEndpoint{}, errors.New("trusttunnel: нет адресов")
	case ep.Username == "" || ep.Password == "":
		return ttEndpoint{}, errors.New("trusttunnel: нет username/password")
	}
	return ep, nil
}

func derChainToPEM(der []byte) (string, error) {
	certs, err := x509.ParseCertificates(der)
	if err != nil {
		return "", fmt.Errorf("trusttunnel: certificate: %w", err)
	}
	var buf bytes.Buffer
	for _, c := range certs {
		_ = pem.Encode(&buf, &pem.Block{Type: "CERTIFICATE", Bytes: c.Raw})
	}
	return buf.String(), nil
}
