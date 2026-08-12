package vlink

import (
	"bytes"
	"encoding/base64"
	"encoding/binary"
	"fmt"
	"io"
	"strings"
)

// TrustTunnel deep link TLV tags (TrustTunnel/DEEP_LINK.md).
const (
	ttTagVersion           uint64 = 0x00
	ttTagHostname          uint64 = 0x01
	ttTagAddresses         uint64 = 0x02
	ttTagCustomSNI         uint64 = 0x03
	ttTagUsername          uint64 = 0x05
	ttTagPassword          uint64 = 0x06
	ttTagSkipVerification  uint64 = 0x07
	ttTagUpstreamProtocol  uint64 = 0x09
	ttTagAntiDPI           uint64 = 0x0A
	ttTagClientRandomPref  uint64 = 0x0B
	ttTagName              uint64 = 0x0C
	ttTagDNSUpstreams      uint64 = 0x0D
)

const (
	ttUpstreamHTTP2 byte = 0x01
	ttUpstreamHTTP3 byte = 0x02
)

type trustTunnelEndpoint struct {
	Hostname           string
	Addresses          []string
	CustomSNI          string
	Username           string
	Password           string
	SkipVerification   bool
	UpstreamProtocol   string // "http2" or "http3"
	AntiDPI            bool
	ClientRandomPrefix string
	Name               string
	DNSUpstreams       []string
}

func decodeTrustTunnelPayload(b64 string) (trustTunnelEndpoint, error) {
	b64 = strings.TrimSpace(b64)
	if b64 == "" {
		return trustTunnelEndpoint{}, fmt.Errorf("trusttunnel: empty payload")
	}
	buf := make([]byte, base64.RawURLEncoding.DecodedLen(len(b64)))
	n, err := base64.RawURLEncoding.Decode(buf, []byte(b64))
	if err != nil {
		// Some panels use standard base64url with padding.
		n, err = base64.URLEncoding.Decode(buf, []byte(b64))
		if err != nil {
			return trustTunnelEndpoint{}, fmt.Errorf("trusttunnel: base64: %w", err)
		}
	}
	return parseTrustTunnelTLV(buf[:n])
}

func parseTrustTunnelTLV(data []byte) (trustTunnelEndpoint, error) {
	ep := trustTunnelEndpoint{UpstreamProtocol: "http2"}
	r := bytes.NewReader(data)
	for {
		tag, err := readTTVarint(r)
		if err != nil {
			if err == io.EOF {
				break
			}
			return trustTunnelEndpoint{}, err
		}
		if err := readTTTag(r, tag, &ep); err != nil {
			return trustTunnelEndpoint{}, err
		}
	}
	if ep.Hostname == "" {
		return trustTunnelEndpoint{}, fmt.Errorf("trusttunnel: missing hostname")
	}
	if len(ep.Addresses) == 0 {
		return trustTunnelEndpoint{}, fmt.Errorf("trusttunnel: missing addresses")
	}
	if ep.Username == "" {
		return trustTunnelEndpoint{}, fmt.Errorf("trusttunnel: missing username")
	}
	if ep.Password == "" {
		return trustTunnelEndpoint{}, fmt.Errorf("trusttunnel: missing password")
	}
	if ep.UpstreamProtocol == "" {
		ep.UpstreamProtocol = "http2"
	}
	return ep, nil
}

func readTTTag(r *bytes.Reader, tag uint64, ep *trustTunnelEndpoint) error {
	switch tag {
	case ttTagVersion:
		_, err := readTTTLVFixed(r, tag, 1)
		return err
	case ttTagHostname:
		v, err := readTTTLVString(r, tag)
		if err != nil {
			return err
		}
		ep.Hostname = v
	case ttTagAddresses:
		v, err := readTTTLVString(r, tag)
		if err != nil {
			return err
		}
		ep.Addresses = append(ep.Addresses, v)
	case ttTagCustomSNI:
		v, err := readTTTLVString(r, tag)
		if err != nil {
			return err
		}
		ep.CustomSNI = v
	case ttTagUsername:
		v, err := readTTTLVString(r, tag)
		if err != nil {
			return err
		}
		ep.Username = v
	case ttTagPassword:
		v, err := readTTTLVString(r, tag)
		if err != nil {
			return err
		}
		ep.Password = v
	case ttTagSkipVerification:
		v, err := readTTTLVBool(r, tag)
		if err != nil {
			return err
		}
		ep.SkipVerification = v
	case ttTagUpstreamProtocol:
		b, err := readTTTLVByte(r, tag)
		if err != nil {
			return err
		}
		switch b {
		case ttUpstreamHTTP2:
			ep.UpstreamProtocol = "http2"
		case ttUpstreamHTTP3:
			ep.UpstreamProtocol = "http3"
		default:
			return fmt.Errorf("trusttunnel: invalid upstream protocol %d", b)
		}
	case ttTagAntiDPI:
		v, err := readTTTLVBool(r, tag)
		if err != nil {
			return err
		}
		ep.AntiDPI = v
	case ttTagClientRandomPref:
		v, err := readTTTLVString(r, tag)
		if err != nil {
			return err
		}
		ep.ClientRandomPrefix = v
	case ttTagName:
		v, err := readTTTLVString(r, tag)
		if err != nil {
			return err
		}
		ep.Name = v
	case ttTagDNSUpstreams:
		v, err := readTTTLVStringArray(r, tag)
		if err != nil {
			return err
		}
		ep.DNSUpstreams = v
	default:
		return skipTTTLV(r, tag)
	}
	return nil
}

func readTTTLVString(r *bytes.Reader, tag uint64) (string, error) {
	b, err := readTTTLVBytes(r, tag)
	if err != nil {
		return "", err
	}
	return string(b), nil
}

func readTTTLVBool(r *bytes.Reader, tag uint64) (bool, error) {
	b, err := readTTTLVByte(r, tag)
	if err != nil {
		return false, err
	}
	return b != 0, nil
}

func readTTTLVByte(r *bytes.Reader, tag uint64) (byte, error) {
	b, err := readTTTLVFixed(r, tag, 1)
	if err != nil {
		return 0, err
	}
	return b[0], nil
}

func readTTTLVFixed(r *bytes.Reader, tag uint64, expect uint64) ([]byte, error) {
	length, err := readTTTLVLength(r, tag)
	if err != nil {
		return nil, err
	}
	if length != expect {
		return nil, fmt.Errorf("trusttunnel: tag %d: expected length %d, got %d", tag, expect, length)
	}
	out := make([]byte, length)
	if _, err := io.ReadFull(r, out); err != nil {
		return nil, err
	}
	return out, nil
}

func readTTTLVBytes(r *bytes.Reader, tag uint64) ([]byte, error) {
	length, err := readTTTLVLength(r, tag)
	if err != nil {
		return nil, err
	}
	if length == 0 {
		return nil, nil
	}
	out := make([]byte, length)
	if _, err := io.ReadFull(r, out); err != nil {
		return nil, err
	}
	return out, nil
}

func readTTTLVStringArray(r *bytes.Reader, tag uint64) ([]string, error) {
	data, err := readTTTLVBytes(r, tag)
	if err != nil {
		return nil, err
	}
	if len(data) == 0 {
		return nil, nil
	}
	inner := bytes.NewReader(data)
	var out []string
	for inner.Len() > 0 {
		length, err := readTTVarint(inner)
		if err != nil {
			return nil, err
		}
		if length > uint64(inner.Len()) {
			return nil, fmt.Errorf("trusttunnel: invalid string array in tag %d", tag)
		}
		item := make([]byte, length)
		if _, err := io.ReadFull(inner, item); err != nil {
			return nil, err
		}
		out = append(out, string(item))
	}
	return out, nil
}

func readTTTLVLength(r *bytes.Reader, tag uint64) (uint64, error) {
	length, err := readTTVarint(r)
	if err != nil {
		return 0, err
	}
	if length > uint64(r.Len()) {
		return 0, fmt.Errorf("trusttunnel: tag %d: length %d exceeds remainder %d", tag, length, r.Len())
	}
	return length, nil
}

func skipTTTLV(r *bytes.Reader, tag uint64) error {
	length, err := readTTTLVLength(r, tag)
	if err != nil {
		return err
	}
	if length == 0 {
		return nil
	}
	if _, err := r.Seek(int64(length), io.SeekCurrent); err != nil {
		return err
	}
	return nil
}

const (
	ttMaxVarInt1 = 63
	ttMaxVarInt2 = 16383
	ttMaxVarInt4 = 1073741823
)

func readTTVarint(r *bytes.Reader) (uint64, error) {
	var scratch [8]byte
	first, err := r.ReadByte()
	if err != nil {
		return 0, err
	}
	sizeCode := first >> 6
	byteLength := 1 << sizeCode
	scratch[0] = first & 0x3f
	if byteLength == 1 {
		return uint64(scratch[0]), nil
	}
	if _, err := io.ReadFull(r, scratch[1:byteLength]); err != nil {
		return 0, err
	}
	switch byteLength {
	case 2:
		return uint64(binary.BigEndian.Uint16(scratch[:2])), nil
	case 4:
		return uint64(binary.BigEndian.Uint32(scratch[:4])), nil
	case 8:
		return binary.BigEndian.Uint64(scratch[:8]), nil
	default:
		return 0, fmt.Errorf("trusttunnel: impossible varint length %d", byteLength)
	}
}
