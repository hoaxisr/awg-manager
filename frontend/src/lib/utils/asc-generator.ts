// ---------------------------------------------------------------------------
// ASC parameter generator — generates all AWG obfuscation parameters
// following AmneziaWG rules.
// ---------------------------------------------------------------------------

/** Random integer in [a, b] inclusive. */
function rnd(a: number, b: number): number {
	return Math.floor(Math.random() * (b - a + 1)) + a;
}

/** Generate unique random H values that don't collide with WG packet types (1-4). */
function generateUniqueH(): number[] {
	const forbidden = new Set([1, 2, 3, 4]);
	const values: number[] = [];
	while (values.length < 4) {
		const v = rnd(5, 2147483647);
		if (!forbidden.has(v) && !values.includes(v)) {
			values.push(v);
		}
	}
	return values;
}

/** Generate H1-H4 as range strings (AWG 2.0, firmware >= 5.1 Alpha 3). */
function generateHRanges(): string[] {
	return Array.from({ length: 4 }, () => {
		const base = rnd(5, 2000000000);
		const spread = rnd(50000000, 500000000);
		const high = Math.min(base + spread, 2147483647);
		return `${base}-${high}`;
	});
}

/** Generate H1-H4 as single values (older firmware). */
function generateHSingle(): string[] {
	return generateUniqueH().map(String);
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
 */
export function generateASCParams(options: {
	extended: boolean;
	hRanges: boolean;
}): GeneratedASCParams {
	const jmin = rnd(50, 500);
	const jmax = rnd(jmin + 50, Math.min(jmin + 500, 1000));

	const h = options.hRanges ? generateHRanges() : generateHSingle();

	const params: GeneratedASCParams = {
		jc: rnd(4, 8),
		jmin,
		jmax,
		s1: rnd(15, 150),
		s2: rnd(15, 150),
		h1: h[0],
		h2: h[1],
		h3: h[2],
		h4: h[3],
	};

	if (options.extended) {
		params.s3 = rnd(15, 150);
		params.s4 = rnd(15, 150);
	}

	return params;
}
