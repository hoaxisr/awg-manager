// ---------------------------------------------------------------------------
// AWG Signature Packet Generators — pure <b 0x...> hex patterns.
//
// Each generator produces realistic protocol headers with random fields
// filled inline as hex bytes. No <r>, <t>, <c> CPS tags — only static
// binary data generated once at creation time.
//
// Total of all I1-I5 fields must stay under 4096 bytes.
// ---------------------------------------------------------------------------

export type ProtocolKey = 'quic_initial' | 'quic_0rtt' | 'tls' | 'dtls' | 'http3' | 'sip';

export const protocols: Record<ProtocolKey, { name: string; description: string }> = {
	quic_initial: { name: 'QUIC Initial', description: 'RFC 9000 — основной протокол для обхода DPI' },
	quic_0rtt: { name: 'QUIC 0-RTT', description: 'Early Data — возобновление сессии' },
	tls: { name: 'TLS 1.3', description: 'Client Hello — HTTPS handshake' },
	dtls: { name: 'DTLS 1.2', description: 'WebRTC, VoIP — медиа-трафик' },
	http3: { name: 'HTTP/3', description: 'QUIC с расширенными типами пакетов' },
	sip: { name: 'SIP', description: 'VoIP сигнализация — REGISTER' },
};

export type SignaturePackets = { i1: string; i2: string; i3: string; i4: string; i5: string };

// --- Helpers ---------------------------------------------------------------

/** Generate N random hex bytes using crypto.getRandomValues. */
function rh(n: number): string {
	const bytes = new Uint8Array(n);
	crypto.getRandomValues(bytes);
	return Array.from(bytes, b => b.toString(16).padStart(2, '0')).join('');
}

/** Integer to hex, padded to byteLen bytes. */
function hexPad(value: number, byteLen: number): string {
	return (value >>> 0).toString(16).padStart(byteLen * 2, '0').slice(-(byteLen * 2));
}

/** Random integer in [a, b] inclusive. */
function rnd(a: number, b: number): number {
	return Math.floor(Math.random() * (b - a + 1)) + a;
}

/** Wrap hex string as CPS <b> tag. */
function bTag(hex: string): string {
	// Ensure even length
	if (hex.length % 2 !== 0) hex += '0';
	return '<b 0x' + hex + '>';
}

/** Pad hex to target byte count with random bytes. */
function padTo(hex: string, targetBytes: number): string {
	const currentBytes = hex.length / 2;
	if (currentBytes >= targetBytes) return hex;
	return hex + rh(targetBytes - currentBytes);
}

/**
 * Calculate byte size of <b 0xHEX> patterns in a string.
 * Counts only hex bytes inside <b> tags.
 */
export function calcByteSize(pattern: string): number {
	let total = 0;
	const re = /<b 0x([0-9a-fA-F]*)>/g;
	let m;
	while ((m = re.exec(pattern)) !== null) {
		total += m[1].length / 2;
	}
	return total;
}

/** Calculate total byte size across all I1-I5. */
export function calcTotalSize(packets: SignaturePackets): number {
	return calcByteSize(packets.i1) + calcByteSize(packets.i2) +
		calcByteSize(packets.i3) + calcByteSize(packets.i4) + calcByteSize(packets.i5);
}

// --- Generators ------------------------------------------------------------

export function getSignaturePackets(protocol: ProtocolKey, mtu: number = 1280): SignaturePackets {
	switch (protocol) {
		case 'quic_initial': return mkQUICInitial(mtu);
		case 'quic_0rtt': return mkQUIC0RTT(mtu);
		case 'tls': return mkTLS(mtu);
		case 'dtls': return mkDTLS(mtu);
		case 'http3': return mkHTTP3(mtu);
		case 'sip': return mkSIP(mtu);
	}
}

// --- QUIC Initial (RFC 9000) -----------------------------------------------
// Long Header (0xC0-C3) + Version + DCID/SCID + Token + CRYPTO frame +
// TLS 1.3 ClientHello + extensions + padding to MTU
function mkQUICInitial(mtu: number): SignaturePackets {
	const dcidLen = rnd(8, 20);
	const scidLen = rnd(0, 20);

	const i1hex = [
		hexPad(0xc0 | rnd(0, 3), 1),       // Flags: QUIC Long Header
		'00000001',                          // Version: QUIC v1
		hexPad(dcidLen, 1),                  // DCID length
		rh(dcidLen),                         // DCID
		hexPad(scidLen, 1),                  // SCID length
		rh(scidLen),                         // SCID
		'00',                                // Token length = 0
		rh(4),                               // Packet Number (4 bytes)
		// CRYPTO frame header
		'060000',                            // Type=CRYPTO, Offset=0
		// TLS 1.3 ClientHello
		'01',                                // Handshake Type = ClientHello
		rh(3),                               // Handshake length (random, DPI sees structure)
		'0303',                              // Client Version = TLS 1.2 (compat)
		rh(32),                              // Client Random
		'20',                                // Session ID length = 32
		rh(32),                              // Session ID
		// Cipher suites: AES128_GCM, AES256_GCM, CHACHA20, SCSV
		'0008130113021303' + '00ff',
		'0100',                              // Compression: null
		// Extensions block (SNI + supported_versions + sig_algs + groups + key_share + ALPN)
		rh(2),                               // Extensions length
		// SNI extension
		'0000', hexPad(rnd(16, 32) + 5, 2), rh(rnd(16, 32)),
		// supported_versions (TLS 1.3)
		'002b00030203040',
		// signature_algorithms
		'00d00140012040308040401050308050501080606010201',
		// supported_groups (x25519, P-256, P-384)
		'000a00080006001d00170018',
		// key_share (x25519)
		'003300260024001d0020',
		rh(32),                              // Key exchange
		// ALPN (h3)
		'0010000500030268330',
		// QUIC transport params
		'039003701048001000003024' + rh(4) + '04048010000005048010000006048010000007048010000008024064090240640e01030f08',
		rh(8),                               // Initial Source Connection ID
	].join('');

	const i1 = bTag(padTo(i1hex, Math.min(mtu, 1280)));

	// I2: QUIC Handshake (ServerHello-like)
	const i2hex = [
		hexPad(0xe0 | rnd(0, 3), 1),        // Handshake packet flags
		'00000001',                          // Version
		hexPad(rnd(8, 12), 1),               // DCID length
		rh(rnd(8, 12)),                      // DCID
		hexPad(rnd(8, 12), 1),               // SCID length
		rh(rnd(8, 12)),                      // SCID
		'06', rh(2),                         // CRYPTO frame
		'020000', rh(1), '0303',             // ServerHello header
		rh(32),                              // Server Random
		'20', rh(32),                        // Session ID
		'130200',                            // Cipher: AES256_GCM, compression: null
		// Extensions: supported_versions + key_share
		'002e002b0002030400330024001d0020',
		rh(32),                              // Key share
	].join('');

	const i2 = bTag(padTo(i2hex, Math.min(rnd(180, 250), mtu)));

	// I3: QUIC Short Header data
	const i3hex = '40' + rh(8) + rh(rnd(60, Math.min(100, mtu - 10)));
	const i3 = bTag(i3hex);

	// I4: Short data
	const i4hex = '40' + rh(8) + rh(rnd(30, 60));
	const i4 = bTag(i4hex);

	// I5: Entropy
	const i5 = bTag(rh(rnd(20, 40)));

	return { i1, i2, i3, i4, i5 };
}

// --- QUIC 0-RTT (Early Data) -----------------------------------------------
// Similar to Initial but with 0-RTT flags (0xD0-D3)
function mkQUIC0RTT(mtu: number): SignaturePackets {
	const dcidLen = rnd(8, 20);
	const scidLen = rnd(0, 20);

	const i1hex = [
		hexPad(0xd0 | rnd(0, 3), 1),        // 0-RTT Long Header
		'00000001',                          // QUIC v1
		hexPad(dcidLen, 1),
		rh(dcidLen),
		hexPad(scidLen, 1),
		rh(scidLen),
		rh(4),                               // Packet Number
		// 0-RTT payload (encrypted application data)
		rh(rnd(40, 80)),
	].join('');

	const i1 = bTag(padTo(i1hex, Math.min(mtu, 1280)));

	// I2: QUIC Initial (server response to 0-RTT)
	const i2hex = [
		hexPad(0xc0 | rnd(0, 3), 1),
		'00000001',
		hexPad(rnd(8, 12), 1), rh(rnd(8, 12)),
		hexPad(rnd(8, 12), 1), rh(rnd(8, 12)),
		'00', rh(4),
		'060000', '020000', rh(1), '0303',
		rh(32), '20', rh(32),
		'130200',
		'002e002b0002030400330024001d0020',
		rh(32),
	].join('');

	const i2 = bTag(padTo(i2hex, Math.min(rnd(180, 250), mtu)));

	const i3hex = '40' + rh(8) + rh(rnd(60, Math.min(100, mtu - 10)));
	const i3 = bTag(i3hex);

	const i4hex = '40' + rh(8) + rh(rnd(30, 60));
	const i4 = bTag(i4hex);

	const i5 = bTag(rh(rnd(20, 40)));

	return { i1, i2, i3, i4, i5 };
}

// --- TLS 1.3 Client Hello --------------------------------------------------
// Record: 16 03 01 + len + Handshake: 01 + len + 03 03 + random + session +
// ciphers + extensions
function mkTLS(mtu: number): SignaturePackets {
	const targetLen = Math.min(rnd(300, 550), mtu);
	// Align to 128 bytes (Chrome fingerprint)
	const recLen = Math.ceil(targetLen / 128) * 128;
	const hsLen = recLen - rnd(4, 9);

	const i1hex = [
		'160301',                            // TLS Record: Handshake, TLS 1.0 (compat)
		hexPad(recLen, 2),                   // Record length
		'01',                                // Handshake Type: ClientHello
		hexPad(hsLen, 3),                    // Handshake length
		'0303',                              // Client Version: TLS 1.2
		rh(32),                              // Client Random
		'20',                                // Session ID length = 32
		rh(32),                              // Session ID
		// Cipher suites
		'0008130113021303' + '00ff',
		'0100',                              // Compression: null
		rh(2),                               // Extensions length
		// SNI
		'0000', hexPad(rnd(14, 30) + 5, 2), rh(rnd(14, 30)),
		// supported_versions
		'002b0003020304',
		// signature_algorithms
		'000d00140012040308040401050308050501080606010201',
		// supported_groups
		'000a00080006001d00170018',
		// key_share (x25519)
		'003300260024001d0020', rh(32),
		// ALPN (h2, http/1.1)
		'0010000e000c02683208687474702f312e31',
	].join('');

	const i1 = bTag(padTo(i1hex, Math.min(recLen, mtu)));

	// I2: TLS ServerHello
	const i2hex = [
		'160303',                            // TLS Record: Handshake, TLS 1.2
		hexPad(rnd(90, 130), 2),             // Record length
		'02',                                // ServerHello
		rh(3),                               // Length
		'0303',                              // Server Version
		rh(32),                              // Server Random
		'20', rh(32),                        // Session ID
		'130200',                            // Cipher + compression
		'002e002b0002030400330024001d0020',
		rh(32),                              // Key share
	].join('');

	const i2 = bTag(i2hex);

	// I3: TLS Application Data
	const i3hex = '170303' + hexPad(rnd(60, 100), 2) + rh(rnd(60, 100));
	const i3 = bTag(i3hex);

	// I4: TLS Application Data (short)
	const i4hex = '170303' + hexPad(rnd(30, 50), 2) + rh(rnd(30, 50));
	const i4 = bTag(i4hex);

	// I5: Entropy
	const i5 = bTag(rh(rnd(20, 40)));

	return { i1, i2, i3, i4, i5 };
}

// --- DTLS 1.2 ClientHello --------------------------------------------------
// Record: 16 fe fd + epoch(2) + seq(6) + len(2) +
// Handshake: 01 + len(3) + msg_seq(2) + frag_offset(3) + frag_len(3) +
// fe fd + random(32) + session_id + cookie + ciphers + extensions
function mkDTLS(mtu: number): SignaturePackets {
	const fragLen = rnd(100, 300);
	const epoch = rnd(0, 255);

	const i1hex = [
		'16fefd',                            // DTLS 1.2 Handshake Record
		hexPad(epoch, 2),                    // Epoch
		rh(6),                               // Sequence Number
		hexPad(fragLen, 2),                  // Fragment length
		'01',                                // ClientHello
		rh(6),                               // Length + msg_seq + frag fields
		'fefd', '0000',                      // DTLS version + cookie placeholder
		rh(4),                               // Message sequence fields
		rh(32),                              // Client Random
		'20', rh(32),                        // Session ID
		'00',                                // Cookie length = 0
		// Cipher suites (16 suites)
		'0020c02bc02fc02cc030c023c027c009c013c00ac014009c009d002f0035000a00ff',
		'0100',                              // Compression: null
		rh(2),                               // Extensions length
		// SNI
		'00000015001300', rh(rnd(10, 20)),
		// supported_groups + ec_point_formats + sig_algs
		'000a00080006001d00170018',
		'000b00040300010200',
		'0d00140012040308040401050308050501080606010201',
		// heartbeat + renegotiation_info
		'000f000101', 'ff01000100',
	].join('');

	const i1 = bTag(padTo(i1hex, Math.min(rnd(350, 450), mtu)));

	// I2: DTLS ServerHello
	const i2hex = [
		'16fefd',
		hexPad(rnd(0, 255), 2), rh(6),
		hexPad(rnd(80, 150), 2),
		'02', rh(6), 'fefd',
		rh(32),                              // Server Random
		'20', rh(32),                        // Session ID
		'c02f00',                            // Cipher + compression
		'002b00020303',                      // supported_versions
		'ff0100010000330024001d0020', rh(32), // renegotiation + key_share
	].join('');

	const i2 = bTag(i2hex);

	// I3: DTLS data record
	const i3hex = '17fefd' + hexPad(rnd(1, 5), 2) + rh(6) + hexPad(rnd(40, 80), 2) + rh(rnd(40, 80));
	const i3 = bTag(i3hex);

	// I4: DTLS short data
	const i4hex = '17fefd' + hexPad(rnd(1, 5), 2) + rh(6) + hexPad(rnd(20, 40), 2) + rh(rnd(20, 40));
	const i4 = bTag(i4hex);

	// I5: Entropy
	const i5 = bTag(rh(rnd(16, 30)));

	return { i1, i2, i3, i4, i5 };
}

// --- HTTP/3 over QUIC -------------------------------------------------------
// Like QUIC Initial but with extended packet type bytes (0xC0-0xE2)
function mkHTTP3(mtu: number): SignaturePackets {
	const ptypes = [0xc0, 0xc1, 0xc2, 0xc3, 0xe0, 0xe1, 0xe2];
	const dcidLen = rnd(8, 20);
	const scidLen = rnd(0, 20);

	const i1hex = [
		hexPad(ptypes[rnd(0, ptypes.length - 1)], 1),
		'00000001',                          // QUIC v1
		hexPad(dcidLen, 1),
		rh(dcidLen),
		hexPad(scidLen, 1),
		rh(scidLen),
		rh(4),                               // Packet Number
		// CRYPTO + TLS ClientHello (same structure as QUIC Initial)
		'060000', '01', rh(3), '0303',
		rh(32),                              // Client Random
		'20', rh(32),                        // Session ID
		'0008130113021303' + '00ff', '0100',
		rh(2),                               // Extensions length
		'0000', hexPad(rnd(16, 32) + 5, 2), rh(rnd(16, 32)),
		'002b0003020304',
		'000d00140012040308040401050308050501080606010201',
		'000a00080006001d00170018',
		'003300260024001d0020', rh(32),
		// ALPN (h3)
		'00100005000302683300',
		// QUIC transport params
		'3900370104800100000302' + rh(2) + '04048010000005048010000006048010000007048010000008024064090240640e01030f08',
		rh(8),
	].join('');

	const i1 = bTag(padTo(i1hex, Math.min(mtu, 1280)));

	// I2: HTTP/3 Handshake
	const i2hex = [
		hexPad(ptypes[rnd(0, ptypes.length - 1)], 1),
		'00000001',
		hexPad(rnd(8, 12), 1), rh(rnd(8, 12)),
		hexPad(rnd(8, 12), 1), rh(rnd(8, 12)),
		'06', rh(2), '020000', rh(1), '0303',
		rh(32), '20', rh(32),
		'130200',
		'002e002b0002030400330024001d0020', rh(32),
	].join('');

	const i2 = bTag(padTo(i2hex, Math.min(rnd(180, 250), mtu)));

	const i3hex = '40' + rh(8) + rh(rnd(60, Math.min(100, mtu - 10)));
	const i3 = bTag(i3hex);

	const i4hex = '40' + rh(8) + rh(rnd(30, 60));
	const i4 = bTag(i4hex);

	const i5 = bTag(rh(rnd(20, 40)));

	return { i1, i2, i3, i4, i5 };
}

// --- SIP REGISTER ----------------------------------------------------------
// ASCII text protocol: "REGISTER sip:<host> SIP/2.0\r\n" + headers
function mkSIP(mtu: number): SignaturePackets {
	const hosts = [
		'sip.voip.ms', 'pbx.sipcentric.com', 'sip.antisip.com',
		'sip.linphone.org', 'sip.zadarma.com', 'proxy.sip.us',
	];
	const host = hosts[rnd(0, hosts.length - 1)];

	// Convert ASCII to hex
	function asciiToHex(s: string): string {
		return Array.from(s, c => c.charCodeAt(0).toString(16).padStart(2, '0')).join('');
	}

	const sipLine = 'REGISTER sip:' + host + ' SIP/2.0\r\n';
	const viaLine = 'Via: SIP/2.0/UDP ' + host + ':5060;branch=z9hG4bK' + rh(8) + '\r\n';
	const callId = 'Call-ID: ' + rh(16) + '@' + host + '\r\n';
	const cseq = 'CSeq: 1 REGISTER\r\n';

	const i1hex = asciiToHex(sipLine + viaLine + callId + cseq);
	const i1 = bTag(padTo(i1hex, Math.min(i1hex.length / 2 + rnd(20, 60), mtu)));

	// I2: SIP 200 OK response
	const resp = 'SIP/2.0 200 OK\r\n' + 'Via: SIP/2.0/UDP ' + host + ':5060\r\n' + 'CSeq: 1 REGISTER\r\n';
	const i2 = bTag(asciiToHex(resp));

	// I3-I5: Shorter SIP fragments / entropy
	const i3hex = asciiToHex('SIP/2.0 ') + rh(rnd(20, 40));
	const i3 = bTag(i3hex);

	const i4 = bTag(rh(rnd(30, 60)));
	const i5 = bTag(rh(rnd(16, 32)));

	return { i1, i2, i3, i4, i5 };
}
