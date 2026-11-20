package fake

import (
	"bytes"
	"crypto/hmac"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/binary"
	"errors"
	"fmt"
	"io"
	"net"
	"slices"
	"time"
)

const (
	TypeHandshakeClient = 0x01

	RandomLen = 32
	// record_type(1) + version(2) + size(2) + handshake_type(1) + uint24_length(3) + client_version(2)
	RandomOffset = 1 + 2 + 2 + 1 + 3 + 2

	// https://datatracker.ietf.org/doc/html/rfc8701#name-grease-values
	// https://medium.com/asecuritysite-when-bob-met-alice/in-cybersecurity-what-is-grease-9f8850558dea
	GreaseMask      = 0x0f0f
	GreaseValueType = 0x0a0a

	sniDNSNamesListType = 0
)

var (
	emptyRandom = [RandomLen]byte{}
	extTypeSNI  = [2]byte{}

	ErrCannotFindCipher = errors.New("cannot find a cipher")
)

type ClientHello struct {
	Random      [RandomLen]byte
	SessionID   []byte
	CipherSuite uint16
}

// ReadClientHello reads a FakeTLS ClientHello and authenticates it against a
// list of candidate secrets, returning the index of the one that matched.
//
// A list rather than a single secret is what lets one proxy serve many clients
// with individually revocable credentials. The cost is one HMAC per candidate,
// over a message that is read exactly once; the hostname is checked before any
// key is tried, so every secret in the list must share a fronting host.
//
// The index is -1 on every error path. ErrBadDigest specifically means "none of
// these secrets", which is the signal the caller turns into domain fronting.
func ReadClientHello(
	conn net.Conn,
	secrets [][]byte,
	hostname string,
	tolerateTimeSkewness time.Duration,
) (*ClientHello, int, error) {
	// This is how FakeTLS is organized:
	//  1. We create sha256 HMAC with a given secret
	//  2. We dump there a whole TLS frame except of the fact that random
	//     is filled with all zeroes
	//  3. Digest is computed. This digest should be XORed with
	//     original client random
	//  4. New digest should be all 0 except of last 4 bytes
	//  5. Last 4 bytes are little endian uint32 of UNIX timestamp when
	//     this message was created.
	clientHelloCopy, handshakeReader, err := parseClientHello(conn)
	if err != nil {
		return nil, -1, fmt.Errorf("cannot read client hello: %w", err)
	}

	hello, err := parseHandshake(handshakeReader)
	if err != nil {
		return nil, -1, fmt.Errorf("cannot parse handshake: %w", err)
	}

	sniHostnames, err := parseSNI(handshakeReader)
	if err != nil {
		return nil, -1, fmt.Errorf("cannot parse SNI: %w", err)
	}

	if !slices.Contains(sniHostnames, hostname) {
		return nil, -1, fmt.Errorf("cannot find %s in %v", hostname, sniHostnames)
	}

	// The digest consumes the buffer, so hold the bytes: every candidate has to
	// HMAC the same message.
	clientHelloBytes := clientHelloCopy.Bytes()

	for idx, secret := range secrets {
		computed, ok := verifyDigest(clientHelloBytes, secret, hello)
		if !ok {
			continue
		}

		timestamp := int64(binary.LittleEndian.Uint32(computed[RandomLen-4:]))
		createdAt := time.Unix(timestamp, 0)

		if tdiff := time.Since(createdAt).Abs(); tdiff > tolerateTimeSkewness {
			return nil, -1, fmt.Errorf("timestamp %q is too old %s", createdAt, tdiff)
		}

		return hello, idx, nil
	}

	return nil, -1, ErrBadDigest
}

// verifyDigest recomputes the FakeTLS HMAC for one candidate secret and reports
// whether it authenticates the handshake, along with the digest the timestamp
// is then read out of.
func verifyDigest(clientHelloBytes []byte, secret []byte, hello *ClientHello) ([]byte, bool) {
	buf := bytes.NewBuffer(clientHelloBytes)

	digest := hmac.New(sha256.New, secret)
	// we write a copy of the handshake with client random all nullified.
	digest.Write(buf.Next(RandomOffset))
	buf.Next(RandomLen)
	digest.Write(emptyRandom[:])
	digest.Write(buf.Bytes())

	computed := digest.Sum(nil)

	for i := range RandomLen {
		computed[i] ^= hello.Random[i]
	}

	return computed, subtle.ConstantTimeCompare(emptyRandom[:RandomLen-4], computed[:RandomLen-4]) == 1
}

func parseHandshake(r io.Reader) (*ClientHello, error) {
	//  A protocol version of "3,3" (meaning TLS 1.2) is given.
	header := [2]byte{}

	if _, err := io.ReadFull(r, header[:]); err != nil {
		return nil, fmt.Errorf("cannot read client version: %w", err)
	}

	hello := &ClientHello{}

	if _, err := io.ReadFull(r, hello.Random[:]); err != nil {
		return nil, fmt.Errorf("cannot read client random: %w", err)
	}

	if _, err := io.ReadFull(r, header[:1]); err != nil {
		return nil, fmt.Errorf("cannot read session ID length: %w", err)
	}

	hello.SessionID = make([]byte, int(header[0]))

	if _, err := io.ReadFull(r, hello.SessionID); err != nil {
		return nil, fmt.Errorf("cannot read session id: %w", err)
	}

	if _, err := io.ReadFull(r, header[:]); err != nil {
		return nil, fmt.Errorf("cannot read cipher suite length: %w", err)
	}

	cipherSuiteLen := int64(binary.BigEndian.Uint16(header[:]))

	// Pick the first non-GREASE cipher suite from the list.
	// Real TLS servers never select GREASE values (RFC 8701, pattern 0x?a?a),
	// so echoing them back is a trivial DPI fingerprint.
	// cipherSuiteLen is in bytes; each cipher suite is 2 bytes.
	for range cipherSuiteLen / 2 {
		if _, err := io.ReadFull(r, header[:]); err != nil {
			return nil, fmt.Errorf("cannot read cipher suite: %w", err)
		}

		if hello.CipherSuite != 0 {
			// do not forget we have to scan until the end
			continue
		}

		if cs := binary.BigEndian.Uint16(header[:]); cs&GreaseMask != GreaseValueType {
			hello.CipherSuite = cs
		}
	}

	if hello.CipherSuite == 0 {
		return nil, ErrCannotFindCipher
	}

	if _, err := io.ReadFull(r, header[:1]); err != nil {
		return nil, fmt.Errorf("cannot read compression methods length: %w", err)
	}

	if _, err := io.CopyN(io.Discard, r, int64(header[0])); err != nil {
		return nil, fmt.Errorf("cannot skip compression methods: %w", err)
	}

	return hello, nil
}

func parseSNI(r io.Reader) ([]string, error) {
	header := [2]byte{}

	if _, err := io.ReadFull(r, header[:]); err != nil {
		return nil, fmt.Errorf("cannot read length of TLS extensions: %w", err)
	}

	extensionsLength := int64(binary.BigEndian.Uint16(header[:]))
	buf := &bytes.Buffer{}
	buf.Grow(int(extensionsLength))

	if _, err := io.CopyN(buf, r, extensionsLength); err != nil {
		return nil, fmt.Errorf("cannot read extensions: %w", err)
	}

	for buf.Len() > 0 {
		// 00 00 - assigned value for extension "server name"
		// 00 18 - 0x18 (24) bytes of "server name" extension data follows
		// 00 16 - 0x16 (22) bytes of first (and only) list entry follows
		// 00 - list entry is type 0x00 "DNS hostname"
		// 00 13 - 0x13 (19) bytes of hostname follows
		// 65 78 61 ... 6e 65 74 - "example.ulfheim.net"

		// 00 00 - assigned value for extension "server name"
		extTypeB := buf.Next(2)
		if len(extTypeB) != 2 {
			return nil, fmt.Errorf("cannot read extension type: %v", extTypeB)
		}

		// 00 18 - 0x18 (24) bytes of "server name" extension data follows
		lengthB := buf.Next(2)
		if len(lengthB) != 2 {
			return nil, fmt.Errorf("cannot read extension %v length: %v", extTypeB, lengthB)
		}
		length := int(binary.BigEndian.Uint16(lengthB))

		extDataB := buf.Next(length)
		if len(extDataB) != length {
			return nil, fmt.Errorf("cannot read extension %v data: len %d != %d", extTypeB, length, len(extDataB))
		}

		if !bytes.Equal(extTypeB, extTypeSNI[:]) {
			continue
		}

		buf.Reset()
		buf.Write(extDataB)

		// 00 16 - 0x16 (22) bytes of first (and only) list entry follows
		lengthB = buf.Next(2)
		if len(lengthB) != 2 {
			return nil, fmt.Errorf("cannot read the length of the SNI record: %v", lengthB)
		}

		length = int(binary.BigEndian.Uint16(lengthB))
		if length == 0 {
			return nil, nil
		}

		listType, err := buf.ReadByte()
		if err != nil {
			return nil, fmt.Errorf("cannot read SNI list type: %w", err)
		}

		// 00 - list entry is type 0x00 "DNS hostname"
		if listType != sniDNSNamesListType {
			return nil, fmt.Errorf("incorrect SNI list type %#x", listType)
		}

		names := []string{}

		for buf.Len() > 0 {
			// 00 13 - 0x13 (19) bytes of hostname follows
			lengthB = buf.Next(2)
			if len(lengthB) != 2 {
				return nil, fmt.Errorf("incorrect length of the hostname: %v", lengthB)
			}
			length = int(binary.BigEndian.Uint16(lengthB))

			name := buf.Next(length)
			if len(name) != length {
				return nil, fmt.Errorf("incorrect length of SNI hostname: len %d != %d", length, len(name))
			}

			names = append(names, string(name))
		}

		return names, nil
	}

	return nil, nil
}
