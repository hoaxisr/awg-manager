// ---------------------------------------------------------------------------
// AWG Signature Profile Catalog
//
// Каталог профилей имитации (CONTEXT.md «Профиль имитации»). Сигнатуру
// собирает бэкенд: POST /api/signature/generate. Ключи = signature.Profiles.
//
// Total of all I1-I5 fields must stay under 4096 bytes.
// ---------------------------------------------------------------------------

export const MAX_SIGNATURE_BYTES = 4096;

export type ProtocolKey = 'quic_initial' | 'stun' | 'dns' | 'dtls' | 'sip';

export const protocols: Record<ProtocolKey, { name: string; description: string }> = {
	quic_initial: { name: 'QUIC Initial', description: 'HTTP/3 — валидный ClientHello с шифрованием по RFC 9001' },
	stun: { name: 'STUN / TURN', description: 'WebRTC ICE — Binding или Allocate с FINGERPRINT' },
	dns: { name: 'DNS Query', description: 'UDP DNS-запрос A/AAAA/HTTPS с EDNS0' },
	dtls: { name: 'DTLS (WebRTC)', description: 'DTLS 1.2 ClientHello с use_srtp' },
	sip: { name: 'SIP', description: 'VoIP — REGISTER и повтор с Digest-авторизацией (I1, I2)' },
};

export interface SignaturePackets {
	i1: string;
	i2: string;
	i3: string;
	i4: string;
	i5: string;
}

const CPS_TAG_RE = /<(\w+)(?:\s+([^>]*))?>/g;

/**
 * Calculate byte size of a CPS pattern (I1–I5).
 * Counts payload inside <b>, <r>, <rc>, <rd> and fixed 4-byte <c>/<t> tags.
 */
export function calcByteSize(pattern: string): number {
	if (!pattern) return 0;

	let total = 0;
	let m: RegExpExecArray | null;
	CPS_TAG_RE.lastIndex = 0;

	while ((m = CPS_TAG_RE.exec(pattern)) !== null) {
		const tag = m[1].toLowerCase();
		const arg = (m[2] ?? '').trim();

		if (tag === 'b') {
			const hexMatch = arg.match(/0x([0-9a-fA-F]*)/);
			if (hexMatch) total += hexMatch[1].length / 2;
		} else if (tag === 'r' || tag === 'rc' || tag === 'rd') {
			const n = Number.parseInt(arg, 10);
			if (Number.isFinite(n) && n > 0) total += n;
		} else if (tag === 'c' || tag === 't') {
			total += 4;
		}
	}

	return total;
}

/** Calculate total byte size across all I1-I5. */
export function calcTotalSize(packets: SignaturePackets): number {
	return (
		calcByteSize(packets.i1) +
		calcByteSize(packets.i2) +
		calcByteSize(packets.i3) +
		calcByteSize(packets.i4) +
		calcByteSize(packets.i5)
	);
}
