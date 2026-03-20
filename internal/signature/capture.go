// Package signature captures real QUIC and TLS handshake packets from a domain
// and returns them in AWG CPS <b 0x...> format for use as I1-I5 signature fields.
package signature

import (
	"bytes"
	"crypto/aes"
	"crypto/cipher"
	"crypto/hmac"
	"crypto/rand"
	"crypto/sha256"
	"crypto/tls"
	"encoding/binary"
	"encoding/hex"
	"fmt"
	"net"
	"strings"
	"sync"
	"time"
)

const (
	captureTimeout = 5 * time.Second
	maxPacketSize  = 1500
	maxPackets     = 5
)

// CaptureResult holds the outcome of a packet capture attempt.
type CaptureResult struct {
	OK      bool   `json:"ok"`
	Source  string `json:"source"` // "quic", "tls", or "error"
	Packets struct {
		I1 string `json:"i1"`
		I2 string `json:"i2"`
		I3 string `json:"i3"`
		I4 string `json:"i4"`
		I5 string `json:"i5"`
	} `json:"packets"`
	Warning string `json:"warning,omitempty"`
}

// NormalizeDomain strips protocol prefix, path, and port from a domain string.
func NormalizeDomain(d string) string {
	return normalizeDomain(d)
}

func normalizeDomain(d string) string {
	d = strings.TrimSpace(d)
	d = strings.TrimPrefix(d, "https://")
	d = strings.TrimPrefix(d, "http://")
	// Remove path
	if idx := strings.IndexByte(d, '/'); idx >= 0 {
		d = d[:idx]
	}
	// Remove port
	if host, _, err := net.SplitHostPort(d); err == nil {
		d = host
	}
	return d
}

// Capture performs a real QUIC packet capture against the given domain.
// Only QUIC is supported — TLS signatures are incompatible with AWG and cause crashes.
func Capture(domain string) CaptureResult {
	domain = normalizeDomain(domain)
	if domain == "" {
		return CaptureResult{Source: "error", Warning: "Пустой домен"}
	}

	// Resolve domain to IP
	ips, err := net.LookupHost(domain)
	if err != nil || len(ips) == 0 {
		return CaptureResult{Source: "error", Warning: "Домен не найден: " + domain}
	}
	ip := ips[0]

	// Capture QUIC Initial exchange
	packets, err := captureQUIC(domain, ip, captureTimeout)
	if err == nil && len(packets) >= 2 {
		var r CaptureResult
		r.OK = true
		r.Source = "quic"
		fillPackets(&r, packets)
		return r
	}

	return CaptureResult{Source: "error", Warning: "Домен " + domain + " не поддерживает QUIC (UDP 443). Выберите другой домен."}
}

// captureQUIC sends a QUIC Initial packet to the domain and collects responses.
func captureQUIC(domain, ip string, timeout time.Duration) ([][]byte, error) {
	initial, err := buildQUICInitial(domain)
	if err != nil {
		return nil, fmt.Errorf("build QUIC Initial: %w", err)
	}

	addr := net.JoinHostPort(ip, "443")
	conn, err := net.DialTimeout("udp", addr, timeout)
	if err != nil {
		return nil, fmt.Errorf("dial UDP %s: %w", addr, err)
	}
	defer conn.Close()

	if err := conn.SetDeadline(time.Now().Add(timeout)); err != nil {
		return nil, err
	}

	// Send our QUIC Initial
	if _, err := conn.Write(initial); err != nil {
		return nil, fmt.Errorf("write QUIC Initial: %w", err)
	}

	// Collect: first packet is what we sent, then server responses
	var packets [][]byte
	packets = append(packets, initial)

	buf := make([]byte, 2048)
	for len(packets) < maxPackets+1 { // +1 because we already have our sent packet
		n, err := conn.Read(buf)
		if err != nil {
			break // timeout or other error — we have what we have
		}
		if n > 0 {
			pkt := make([]byte, n)
			copy(pkt, buf[:n])
			packets = append(packets, pkt)
		}
	}

	if len(packets) < 2 {
		return nil, fmt.Errorf("no QUIC response from %s", ip)
	}

	return packets, nil
}


// buildQUICInitial constructs a properly AEAD-encrypted QUIC v1 Initial packet
// containing a real TLS 1.3 ClientHello with the given domain as SNI.
// Encryption follows RFC 9001: Initial keys derived from DCID via HKDF,
// payload encrypted with AES-128-GCM, header protected with AES-ECB.
func buildQUICInitial(domain string) ([]byte, error) {
	clientHello, err := buildTLSClientHello(domain)
	if err != nil {
		return nil, err
	}

	// Strip TLS record header (5 bytes: type + version + length)
	if len(clientHello) < 5 {
		return nil, fmt.Errorf("ClientHello too short: %d bytes", len(clientHello))
	}
	chPayload := clientHello[5:]

	// Generate random DCID (8 bytes)
	dcid := make([]byte, 8)
	if _, err := rand.Read(dcid); err != nil {
		return nil, err
	}

	// Derive Initial keys from DCID (RFC 9001 §5.2)
	clientKey, clientIV, clientHP, err := deriveInitialKeys(dcid)
	if err != nil {
		return nil, fmt.Errorf("derive keys: %w", err)
	}

	// --- Build CRYPTO frame ---
	// type(0x06) + offset(0x00) + length(varint) + ClientHello
	cryptoFrame := []byte{0x06, 0x00}
	cryptoFrame = append(cryptoFrame, quicVarint(len(chPayload))...)
	cryptoFrame = append(cryptoFrame, chPayload...)

	// Packet number = 0 (4 bytes)
	pktNum := []byte{0x00, 0x00, 0x00, 0x00}
	pktNumLen := len(pktNum)

	// Payload to encrypt = CRYPTO frame + PADDING (to reach 1200 bytes minimum)
	// We need to calculate header size first to know how much padding we need
	headerSizeEstimate := 1 + 4 + 1 + len(dcid) + 1 + 1 + 2 + pktNumLen // flags+ver+dcid+scid+token+length+pn
	minPayloadBytes := 1200 - headerSizeEstimate - 16                     // minus AEAD tag
	plaintext := cryptoFrame
	if len(plaintext) < minPayloadBytes {
		plaintext = append(plaintext, make([]byte, minPayloadBytes-len(plaintext))...)
	}

	// --- Encrypt payload with AES-128-GCM ---
	block, err := aes.NewCipher(clientKey)
	if err != nil {
		return nil, err
	}
	gcm, err := cipher.NewGCM(block)
	if err != nil {
		return nil, err
	}

	// Nonce = IV XOR packet number (left-padded to 12 bytes)
	nonce := make([]byte, 12)
	copy(nonce, clientIV)
	// XOR last 4 bytes with packet number (which is 0, so nonce = IV)
	for i := 0; i < pktNumLen; i++ {
		nonce[12-pktNumLen+i] ^= pktNum[i]
	}

	// --- Build unprotected header (for AAD) ---
	var header []byte
	// Flags: 0xC3 = Long Header (1) + Fixed (1) + Initial (00) + PN Length (11 = 4 bytes)
	header = append(header, 0xC3)
	header = append(header, 0x00, 0x00, 0x00, 0x01) // Version: QUIC v1
	header = append(header, byte(len(dcid)))
	header = append(header, dcid...)
	header = append(header, 0x00) // SCID length = 0
	header = append(header, 0x00) // Token length = 0

	// Length = packet number (4) + plaintext + AEAD tag (16)
	lengthVal := pktNumLen + len(plaintext) + gcm.Overhead()
	header = append(header, quicVarint(lengthVal)...)

	// Packet number (unprotected at this point)
	header = append(header, pktNum...)

	// Encrypt: AAD = header, plaintext = CRYPTO frame + padding
	ciphertext := gcm.Seal(nil, nonce, plaintext, header)

	// --- Apply Header Protection (RFC 9001 §5.4) ---
	// Sample: first 16 bytes of ciphertext (starting from 4th byte after PN)
	// Since PN offset = len(header) - pktNumLen and sample starts at PN offset + 4
	sample := ciphertext[0:16] // pktNumLen=4, so sample starts at offset 4-4=0 of ciphertext

	hpBlock, err := aes.NewCipher(clientHP)
	if err != nil {
		return nil, err
	}
	mask := make([]byte, aes.BlockSize)
	hpBlock.Encrypt(mask, sample)

	// Apply mask to first byte: for long header, mask lower 4 bits
	protectedHeader := make([]byte, len(header))
	copy(protectedHeader, header)
	protectedHeader[0] ^= mask[0] & 0x0F

	// Apply mask to packet number bytes
	pnOffset := len(header) - pktNumLen
	for i := 0; i < pktNumLen; i++ {
		protectedHeader[pnOffset+i] ^= mask[1+i]
	}

	// Assemble final packet: protected header + ciphertext
	pkt := append(protectedHeader, ciphertext...)

	return pkt, nil
}

// --- QUIC Initial Key Derivation (RFC 9001 §5.2) ---

// QUIC v1 initial salt (RFC 9001 §5.2)
var quicV1InitialSalt, _ = hex.DecodeString("38762cf7f55934b34d179ae6a4c80cadccbb7f0a")

// deriveInitialKeys derives the client Initial key, IV, and HP key from a DCID.
func deriveInitialKeys(dcid []byte) (key, iv, hp []byte, err error) {
	// initial_secret = HKDF-Extract(initial_salt, DCID)
	initialSecret := hkdfExtract(quicV1InitialSalt, dcid)

	// client_initial_secret = HKDF-Expand-Label(initial_secret, "client in", "", 32)
	clientSecret := hkdfExpandLabel(initialSecret, "client in", nil, 32)

	// key = HKDF-Expand-Label(client_secret, "quic key", "", 16)
	key = hkdfExpandLabel(clientSecret, "quic key", nil, 16)
	// iv = HKDF-Expand-Label(client_secret, "quic iv", "", 12)
	iv = hkdfExpandLabel(clientSecret, "quic iv", nil, 12)
	// hp = HKDF-Expand-Label(client_secret, "quic hp", "", 16)
	hp = hkdfExpandLabel(clientSecret, "quic hp", nil, 16)

	return key, iv, hp, nil
}

// hkdfExtract performs HKDF-Extract (RFC 5869) using HMAC-SHA256.
func hkdfExtract(salt, ikm []byte) []byte {
	mac := hmac.New(sha256.New, salt)
	mac.Write(ikm)
	return mac.Sum(nil)
}

// hkdfExpandLabel performs HKDF-Expand-Label as defined in TLS 1.3 (RFC 8446 §7.1).
// Label is prefixed with "tls13 " automatically.
func hkdfExpandLabel(secret []byte, label string, context []byte, length int) []byte {
	// HkdfLabel = length(2) + "tls13 " + label + context_length(1) + context
	fullLabel := "tls13 " + label
	var hkdfLabel []byte
	hkdfLabel = append(hkdfLabel, byte(length>>8), byte(length))
	hkdfLabel = append(hkdfLabel, byte(len(fullLabel)))
	hkdfLabel = append(hkdfLabel, []byte(fullLabel)...)
	hkdfLabel = append(hkdfLabel, byte(len(context)))
	hkdfLabel = append(hkdfLabel, context...)

	return hkdfExpand(secret, hkdfLabel, length)
}

// hkdfExpand performs HKDF-Expand (RFC 5869) using HMAC-SHA256.
func hkdfExpand(prk, info []byte, length int) []byte {
	hashLen := sha256.Size
	n := (length + hashLen - 1) / hashLen
	var out []byte
	var prev []byte

	for i := 1; i <= n; i++ {
		mac := hmac.New(sha256.New, prk)
		mac.Write(prev)
		mac.Write(info)
		mac.Write([]byte{byte(i)})
		prev = mac.Sum(nil)
		out = append(out, prev...)
	}

	return out[:length]
}

// quicVarint encodes an integer as a QUIC variable-length integer.
func quicVarint(v int) []byte {
	if v < 64 {
		return []byte{byte(v)}
	}
	if v < 16384 {
		var b [2]byte
		binary.BigEndian.PutUint16(b[:], uint16(v)|0x4000)
		return b[:]
	}
	var b [4]byte
	binary.BigEndian.PutUint32(b[:], uint32(v)|0x80000000)
	return b[:]
}

// buildTLSClientHello uses crypto/tls with net.Pipe() to generate a real
// TLS 1.3 ClientHello with the given domain as SNI.
func buildTLSClientHello(domain string) ([]byte, error) {
	clientConn, serverConn := net.Pipe()
	defer clientConn.Close()
	defer serverConn.Close()

	var (
		chBytes []byte
		chErr   error
		wg      sync.WaitGroup
	)

	wg.Add(1)
	go func() {
		defer wg.Done()
		// Set read deadline so we don't block forever on the synchronous pipe
		_ = serverConn.SetReadDeadline(time.Now().Add(1 * time.Second))
		// Read raw bytes from the pipe in a loop — this is the ClientHello
		var buf bytes.Buffer
		tmp := make([]byte, 4096)
		for {
			n, err := serverConn.Read(tmp)
			buf.Write(tmp[:n])
			if err != nil {
				break
			}
		}
		if buf.Len() == 0 {
			chErr = fmt.Errorf("read ClientHello from pipe: no data")
			return
		}
		chBytes = buf.Bytes()
		// Close server side to unblock the client handshake
		serverConn.Close()
	}()

	tlsConn := tls.Client(clientConn, &tls.Config{
		ServerName:         domain,
		InsecureSkipVerify: true,
		NextProtos:         []string{"h3"},
		MinVersion:         tls.VersionTLS13,
		MaxVersion:         tls.VersionTLS13,
	})

	// Handshake will write the ClientHello and then fail — that's expected
	_ = tlsConn.Handshake()

	wg.Wait()

	if chErr != nil {
		return nil, chErr
	}
	if len(chBytes) == 0 {
		return nil, fmt.Errorf("empty ClientHello")
	}

	return chBytes, nil
}

// fillPackets converts raw packet bytes to CPS <b 0x...> format and fills I1-I5.
func fillPackets(r *CaptureResult, packets [][]byte) {
	fields := []*string{&r.Packets.I1, &r.Packets.I2, &r.Packets.I3, &r.Packets.I4, &r.Packets.I5}

	for i := 0; i < len(fields) && i < len(packets); i++ {
		pkt := packets[i]
		if len(pkt) > maxPacketSize {
			pkt = pkt[:maxPacketSize]
		}
		*fields[i] = "<b 0x" + hex.EncodeToString(pkt) + ">"
	}
}
