export type ProtocolKey = 'quic' | 'quic_google' | 'dtls' | 'stun' | 'steam' | 'dns' | 'ntp';

export const protocols: Record<ProtocolKey, { name: string; description: string }> = {
	quic: { name: 'QUIC', description: 'Универсальный QUIC Initial пакет (~550 байт)' },
	quic_google: { name: 'QUIC Google', description: 'Имитация google.com — YouTube, Gmail (~550 байт)' },
	dtls: { name: 'DTLS 1.2', description: 'WebRTC, VoIP — легитимный медиа-трафик (~400 байт)' },
	stun: { name: 'STUN', description: 'WebRTC NAT traversal — очень частый (~76 байт)' },
	steam: { name: 'Steam', description: 'CS2, Dota 2 — игровые серверы Valve (~120 байт)' },
	dns: { name: 'DNS', description: 'DNS запросы — всегда разрешены (~68 байт)' },
	ntp: { name: 'NTP', description: 'Синхронизация времени — редко блокируется (48 байт)' },
};

export type SignaturePackets = { i1: string; i2: string; i3: string; i4: string; i5: string };

// ---------------------------------------------------------------------------
// Realistic protocol signature packets.
//
// Structure uses AmneziaWG CPS tags:
//   <b 0xHEX>  — static bytes (protocol fingerprint)
//   <r N>      — N random bytes
//   <t>        — 4-byte UNIX timestamp
//   <c>        — 4-byte packet counter
//
// Total of all I1-I5 fields must stay under 4096 bytes.
// ---------------------------------------------------------------------------

export function getSignaturePackets(protocol: ProtocolKey): SignaturePackets {
	switch (protocol) {
		case 'quic':
			return quicGeneric();
		case 'quic_google':
			return quicGoogle();
		case 'dtls':
			return dtls();
		case 'stun':
			return stun();
		case 'steam':
			return steam();
		case 'dns':
			return dns();
		case 'ntp':
			return ntp();
	}
}

// --- QUIC v1 Initial (~550 bytes) ------------------------------------------
// Long Header + CRYPTO frame + TLS 1.3 ClientHello with extensions + padding
function quicGeneric(): SignaturePackets {
	// I1: QUIC Initial with TLS ClientHello (~550 bytes)
	// Header: c0(flags) 00000001(QUICv1) 08(DCID_len)
	// CRYPTO: 06(type) 00(offset) 420a(len=522)
	// TLS: 01(ClientHello) 000206(len=518) 0303(TLSv1.2)
	// Ciphers: AES128_GCM, AES256_GCM, CHACHA20, SCSV
	// Extensions: SNI(random), supported_versions, sig_algs, groups, key_share, ALPN(h3), QUIC transport params, padding
	const i1 = [
		'<b 0xc00000000108>',          // QUIC Long Header + DCID_len=8
		'<r 8>',                        // DCID
		'<b 0x000042120600420a010002060303>', // SCID=0, Token=0, PayloadLen, CRYPTO frame, TLS header
		'<r 32>',                       // Client Random
		'<b 0x20>',                     // Session ID length=32
		'<r 32>',                       // Session ID
		// Cipher suites(4) + compression + extensions_len(437) + SNI header(name_len=16)
		'<b 0x000813011302130300ff010001b5000000150013000010>',
		'<r 16>',                       // SNI hostname
		// supported_versions(TLS1.3) + sig_algs(9 algos) + supported_groups(x25519,P-256,P-384) + key_share header(x25519,len=32)
		'<b 0x002b0003020304000d00140012040308040401050308050501080606010201000a00080006001d00170018003300260024001d0020>',
		'<r 32>',                       // Key exchange data
		// ALPN(h3) + QUIC transport params + initial_source_connection_id header
		'<b 0x00100005000302683300390037010480010000030245b404048010000005048010000006048010000007048010000008024064090240640e01030f08>',
		'<r 8>',                        // Source connection ID
		'<b 0x00150105>',               // TLS padding extension, len=261
		'<r 261>',                      // Padding
	].join('');

	// I2: QUIC Handshake (ServerHello-like, ~180 bytes)
	const i2 = [
		'<b 0xe00000000108>',           // Handshake packet flags + version + DCID_len
		'<r 8>',                        // DCID
		'<b 0x08>',                     // SCID_len=8
		'<r 8>',                        // SCID
		'<b 0x409b0600407a020000760303>', // PayloadLen, CRYPTO, ServerHello header
		'<r 32>',                       // Server Random
		'<b 0x20>',                     // Session ID length=32
		'<r 32>',                       // Session ID
		// cipher(AES256_GCM) + compression + extensions(supported_versions+key_share)
		'<b 0x130200002e002b0002030400330024001d0020>',
		'<r 32>',                       // Key share
		'<r 29>',                       // QUIC PADDING frames
	].join('');

	// I3: QUIC Short Header data (~100 bytes)
	const i3 = '<b 0x40><r 8><c><r 87>';

	// I4: Short data (~60 bytes)
	const i4 = '<b 0x40><r 8><t><r 47>';

	// I5: Minimal (~40 bytes)
	const i5 = '<r 36><t>';

	return { i1, i2, i3, i4, i5 };
}

// --- QUIC Google (~550 bytes) -----------------------------------------------
// Same as generic QUIC but with explicit google.com SNI and h3+h2 ALPN
function quicGoogle(): SignaturePackets {
	// I1: google.com SNI in TLS ClientHello
	const i1 = [
		'<b 0xc00000000108>',
		'<r 8>',
		'<b 0x000042120600420a010002060303>',
		'<r 32>',
		'<b 0x20>',
		'<r 32>',
		// Ciphers + compression + ext_len + SNI with "www.google.com" (14 bytes)
		'<b 0x000813011302130300ff010001b500000013001100000e7777772e676f6f676c652e636f6d>',
		// supported_versions + sig_algs + groups + key_share header
		'<b 0x002b0003020304000d00140012040308040401050308050501080606010201000a00080006001d00170018003300260024001d0020>',
		'<r 32>',
		// ALPN(h3+h2) + QUIC transport params + ISCI
		'<b 0x00100008000602683302683200390037010480010000030245b404048010000005048010000006048010000007048010000008024064090240640e01030f08>',
		'<r 8>',
		'<b 0x00150104>',               // TLS padding, len=260
		'<r 260>',
	].join('');

	const i2 = [
		'<b 0xe00000000108>',
		'<r 8>',
		'<b 0x08>',
		'<r 8>',
		'<b 0x409b0600407a020000760303>',
		'<r 32>',
		'<b 0x20>',
		'<r 32>',
		'<b 0x130200002e002b0002030400330024001d0020>',
		'<r 32>',
		'<r 29>',
	].join('');

	const i3 = '<b 0x40><r 8><c><r 87>';
	const i4 = '<b 0x40><r 8><t><r 47>';
	const i5 = '<r 36><t>';

	return { i1, i2, i3, i4, i5 };
}

// --- DTLS 1.2 ClientHello (~400 bytes) --------------------------------------
// Record layer + Handshake header + ClientHello with cipher suites + extensions
function dtls(): SignaturePackets {
	// I1: DTLS ClientHello (~400 bytes)
	// Record: 16(Handshake) fefd(DTLS1.2) epoch=0 seq=1 len=387
	// Handshake: 01(ClientHello) len=375 msg_seq=0 frag=0 frag_len=375
	// Body: fefd(version) + random + session_id + cookie=0 + 16 cipher suites + compression + extensions
	const i1 = [
		// Record header(13) + Handshake header(12) + DTLS version
		'<b 0x16fefd00000000000000010183010001770000000000000177fefd>',
		'<r 32>',                       // Client Random
		'<b 0x20>',                     // Session ID len=32
		'<r 32>',                       // Session ID
		// cookie=0 + cipher_suites(16 suites) + compression + ext_len(269) + SNI header
		'<b 0x000020c02bc02fc02cc030c023c027c009c013c00ac014009c009d002f0035000a00ff0100010d000000150013000010>',
		'<r 16>',                       // SNI hostname
		// groups + ec_point_formats + sig_algs + heartbeat + renegotiation_info + padding_ext header(len=186)
		'<b 0x000a00080006001d00170018000b000403000102000d00140012040308040401050308050501080606010201000f000101ff01000100001500ba>',
		'<r 186>',                      // Padding
	].join('');

	// I2: DTLS ServerHello (~160 bytes)
	const i2 = [
		'<b 0x16fefd00000000000000020096020000860000000000000086fefd>',
		'<r 32>',                       // Server Random
		'<b 0x20>',                     // Session ID len=32
		'<r 32>',                       // Session ID
		'<b 0xc02f00000a002b00020303ff0100010000330024001d0020>',
		'<r 32>',                       // Key share
		'<r 5>',                        // Padding
	].join('');

	// I3: DTLS data record (~80 bytes)
	const i3 = '<b 0x17fefd0001><r 6><b 0x0040><r 64>';

	// I4: DTLS alert/short (~48 bytes)
	const i4 = '<b 0x17fefd0001><r 6><b 0x0020><r 32>';

	// I5: Minimal (~24 bytes)
	const i5 = '<b 0x17fefd><r 6><t><r 10>';

	return { i1, i2, i3, i4, i5 };
}

// --- STUN Binding Request (~76 bytes) ----------------------------------------
function stun(): SignaturePackets {
	// I1: STUN Binding Request with attributes (~76 bytes)
	// Header: type=0001(Binding) len=56 magic=2112a442 + 12-byte transaction ID
	// Attrs: SOFTWARE(32) + PRIORITY(8) + ICE-CONTROLLED(12) + FINGERPRINT(8)
	const i1 = [
		'<b 0x000100382112a442>',       // STUN header (type + length + magic)
		'<r 12>',                       // Transaction ID
		'<b 0x8022001c>',               // SOFTWARE attr header (len=28)
		'<r 28>',                       // SOFTWARE value
		'<b 0x002400046e7f00ff>',       // PRIORITY attr (value=0x6e7f00ff)
		'<b 0x802900080000000000000001>', // ICE-CONTROLLED (tie-breaker)
		'<b 0x80280004>',               // FINGERPRINT attr header
		'<r 4>',                        // FINGERPRINT CRC32
	].join('');

	// I2: STUN Binding Response (~68 bytes)
	const i2 = [
		'<b 0x010100302112a442>',       // Response header
		'<r 12>',                       // Transaction ID
		'<b 0x002000080001><r 2><b 0x2112a442>', // XOR-MAPPED-ADDRESS
		'<b 0x80220010>',               // SOFTWARE
		'<r 16>',                       // SOFTWARE value
		'<b 0x80280004>',               // FINGERPRINT
		'<r 4>',                        // CRC32
	].join('');

	// I3-I5: Shorter STUN patterns
	const i3 = '<b 0x000100082112a442><r 12><b 0x80280004><r 4>';
	const i4 = '<b 0x2112a442><r 16><t>';
	const i5 = '<r 16><c>';

	return { i1, i2, i3, i4, i5 };
}

// --- Steam Source Engine Query (~120 bytes) ----------------------------------
function steam(): SignaturePackets {
	// I1: Source Engine A2S_INFO query + response-like data (~120 bytes)
	const i1 = [
		'<b 0xffffffff>',               // Simple header
		'<b 0x54536f7572636520456e67696e6520517565727900>', // "Source Engine Query\0"
		'<r 32>',                       // Payload data
		'<b 0x01>',                     // Protocol version
		'<r 30>',                       // Server info fields
		'<b 0x00646c0064>',             // Null-terminated strings + player counts
		'<r 28>',                       // Map name + game desc
		'<t>',                          // Timestamp
	].join('');

	// I2: A2S_PLAYER response (~80 bytes)
	const i2 = [
		'<b 0xffffffff44>',             // Header + A2S_PLAYER response type
		'<b 0x04>',                     // Player count
		'<r 60>',                       // Player data
		'<r 8>',                        // Score/duration
		'<t>',                          // Timestamp
		'<c>',                          // Counter
	].join('');

	// I3: Challenge response (~40 bytes)
	const i3 = '<b 0xffffffff41><r 4><b 0xffffffff><r 24><t>';

	// I4: Short query (~32 bytes)
	const i4 = '<b 0xffffffff><r 20><t><c>';

	// I5: Minimal (~20 bytes)
	const i5 = '<b 0xffffffff><r 12><t>';

	return { i1, i2, i3, i4, i5 };
}

// --- DNS Query (~68 bytes) ---------------------------------------------------
function dns(): SignaturePackets {
	// I1: DNS query for "www.google.com" with EDNS0 OPT (~68 bytes)
	const i1 = [
		'<r 2>',                        // Transaction ID
		// Flags(standard query, RD=1) + QD=1, AN=0, NS=0, AR=1
		'<b 0x01000001000000000001>',
		// QNAME: \x03www\x06google\x03com\x00 + QTYPE=A + QCLASS=IN
		'<b 0x0377777706676f6f676c6503636f6d0000010001>',
		// EDNS0 OPT: root(00) TYPE=OPT(0029) UDP=4096(1000) extRCODE=0 ver=0 DO=0 rdlen=12
		'<b 0x00002910000000000c>',
		// EDNS Cookie option: code=000a len=0008 + 8 random bytes
		'<b 0x000a0008>',
		'<r 8>',                        // Cookie
	].join('');

	// I2: DNS response (~60 bytes)
	const i2 = [
		'<r 2>',                        // Transaction ID
		'<b 0x81800001000100000000>',    // Response flags + counts
		'<b 0x0377777706676f6f676c6503636f6d0000010001>', // Query echo
		'<b 0xc00c00010001000000e00004>', // Answer: pointer, A, IN, TTL=224, rdlen=4
		'<r 4>',                        // IP address
	].join('');

	// I3: DNS query short (~40 bytes)
	const i3 = '<r 2><b 0x01000001000000000000><b 0x03646e7306676f6f676c6503636f6d0000010001>';

	// I4: Short with timestamp (~24 bytes)
	const i4 = '<r 2><b 0x0100000100000000000003777777><r 4><t>';

	// I5: Minimal (~16 bytes)
	const i5 = '<r 2><b 0x0100><r 8><t>';

	return { i1, i2, i3, i4, i5 };
}

// --- NTP v4 Client Request (48 bytes) ----------------------------------------
function ntp(): SignaturePackets {
	// I1: NTP Client mode packet (exactly 48 bytes)
	// Flags: LI=0 VN=4 Mode=3(client) = 0x23
	// Stratum=0, Poll=6, Precision=-20(0xec)
	// Root delay/dispersion/refID = 0
	// Timestamps: ref=0, origin=0, receive=0, transmit=now
	const i1 = [
		'<b 0x230006ec>',               // LI+VN+Mode, Stratum, Poll, Precision
		'<b 0x00000000>',               // Root Delay
		'<b 0x00000000>',               // Root Dispersion
		'<b 0x00000000>',               // Reference ID
		'<b 0x0000000000000000>',        // Reference Timestamp
		'<b 0x0000000000000000>',        // Origin Timestamp
		'<b 0x0000000000000000>',        // Receive Timestamp
		'<t>',                          // Transmit Timestamp (seconds)
		'<r 4>',                        // Transmit Timestamp (fraction)
	].join('');

	// I2: NTP Server response (48 bytes)
	const i2 = [
		'<b 0x240106ec>',               // LI=0 VN=4 Mode=4(server), Stratum=1
		'<b 0x00000001>',               // Root Delay
		'<b 0x00000001>',               // Root Dispersion
		'<b 0x4750530000000000>',        // Reference ID "GPS\0" + ref timestamp
		'<t><r 4>',                     // Origin timestamp
		'<t><r 4>',                     // Receive timestamp
		'<t><r 4>',                     // Transmit timestamp
		'<r 4>',                        // Extra fraction
	].join('');

	// I3: NTP symmetric (~48 bytes)
	const i3 = '<b 0x230006ec000000000000000000000000><r 8><t><r 4><t><r 4><t><r 4>';

	// I4: Short (~32 bytes)
	const i4 = '<b 0x230006ec00000000000000000000000000000000><t><r 4>';

	// I5: Minimal
	const i5 = '<t><r 4><b 0x230006ec>';

	return { i1, i2, i3, i4, i5 };
}
