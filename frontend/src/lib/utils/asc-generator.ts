// ---------------------------------------------------------------------------
// ASC parameter generator — generates all AWG obfuscation parameters
// following AmneziaWG rules.
//
// Reference: https://github.com/vadim-khristenko/AmneziaWG-Architect
// ---------------------------------------------------------------------------

/** Random integer in [a, b] inclusive. */
function rnd(a: number, b: number): number {
	return Math.floor(Math.random() * (b - a + 1)) + a;
}

/**
 * Generate H1-H4 as range strings (AWG 2.0).
 * Uses fixed well-separated bases (~100M, ~1.2B, ~2.4B, ~3.6B) with ±500K spread
 * to guarantee non-overlapping ranges.
 */
function generateHRanges(): string[] {
	const bases = [100_000_000, 1_200_000_000, 2_400_000_000, 3_600_000_000];
	return bases.map(base => {
		const offset = rnd(0, 4_000_000);
		const spread = rnd(100_000, 500_000);
		const start = base + offset;
		return `${start}-${start + spread}`;
	});
}

/**
 * Generate H1-H4 as single values (AWG 1.x, older firmware).
 * Same well-separated bases with small random offset.
 */
function generateHSingle(): string[] {
	const bases = [100_000_000, 1_200_000_000, 2_400_000_000, 3_600_000_000];
	return bases.map(base => String(base + rnd(0, 4_000_000)));
}

export interface GeneratedASCParams {
	jc: number;
	jmin: number;
	jmax: number;
	s1: number;
	s2: number;
	h1: string;
	h2: string;
	h3: string;
	h4: string;
	// Extended (if supported)
	s3?: number;
	s4?: number;
}

/**
 * Generate all numeric/header ASC parameters.
 * I1-I5 are NOT included — they should be obtained via captureSignature().
 *
 * Constraints (per AmneziaWG spec + Keenetic NDMS validation):
 *   Jc:     3-10
 *   Jmin:   64+
 *   Jmax:   Jmin+1 to 1280, must be > Jmin
 *   S1:     15-64
 *   S2:     15-64 (S1+56 must not equal S2)
 *   S3:     8-64
 *   S4:     6-32
 *   H1-H4:  non-overlapping, unique, not 1-4
 */
export function generateASCParams(options: {
	extended: boolean;
	hRanges: boolean;
}): GeneratedASCParams {
	const jmin = rnd(64, 128);
	const jmax = Math.min(1280, rnd(256, 512));

	const h = options.hRanges ? generateHRanges() : generateHSingle();

	// Generate S1/S2 with constraint: S1+56 ≠ S2
	const s1 = rnd(15, 32);
	let s2 = rnd(15, 32);
	while (s1 + 56 === s2) {
		s2 = rnd(15, 32);
	}

	const params: GeneratedASCParams = {
		jc: rnd(3, 8),
		jmin,
		jmax,
		s1,
		s2,
		h1: h[0],
		h2: h[1],
		h3: h[2],
		h4: h[3],
	};

	if (options.extended) {
		params.s3 = rnd(8, 24);
		params.s4 = rnd(6, 18);
	}

	return params;
}
