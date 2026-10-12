import { m } from '$lib/i18n';
import { formatIpCidrForList, toAsciiHostname } from '$lib/utils/singboxInlineRules';

const GEOSITE_RE = /^geosite:([A-Za-z0-9_-]+)$/i;
const GEOIP_RE = /^geoip:([A-Za-z0-9_-]+)$/i;

/** Geosite .dat RootDomain lines → smart-list suffix without leading dot. */
function normalizeGeositeExpandLine(line: string): string {
	const t = line.trim();
	const lower = t.toLowerCase();
	if (
		lower.startsWith('domain_regex:') ||
		lower.startsWith('regex:') ||
		lower.startsWith('domain_keyword:') ||
		lower.startsWith('keyword:') ||
		lower.startsWith('domain:')
	) {
		return t;
	}
	let host = t;
	if (host.startsWith('.')) host = host.slice(1);
	else if (host.startsWith('*.')) host = host.slice(2);
	return toAsciiHostname(host) ?? host;
}

export type GeoExpandFn = (kind: 'geosite' | 'geoip', tag: string) => Promise<string[]>;

export interface GeoExpandResult {
	text: string;
	warnings: string[];
}

/** Replace geosite:/geoip: lines with entries from geo .dat files. */
export async function expandGeoLinesInInput(
	input: string,
	expand: GeoExpandFn,
): Promise<GeoExpandResult> {
	const warnings: string[] = [];
	const out: string[] = [];

	for (const rawLine of input.split(/\r?\n/)) {
		const line = rawLine.trim();
		if (line === '' || line.startsWith(';') || line.startsWith('#') || line.startsWith('//')) {
			out.push(rawLine);
			continue;
		}

		const stripped = stripInlineComment(line);
		const geosite = stripped.match(GEOSITE_RE);
		if (geosite) {
			const tag = geosite[1];
			try {
				const items = await expand('geosite', tag);
				if (items.length === 0) {
					warnings.push(m.inline_geo_empty_tag({ kind: 'geosite', tag }));
				} else {
					warnings.push(m.inline_geo_expanded({ kind: 'geosite', tag, count: items.length }));
					out.push(...items.map(normalizeGeositeExpandLine));
				}
			} catch (e) {
				warnings.push(`geosite:${tag}: ${(e as Error).message}`);
				out.push(rawLine);
			}
			continue;
		}

		const geoip = stripped.match(GEOIP_RE);
		if (geoip) {
			const tag = geoip[1];
			try {
				const items = await expand('geoip', tag);
				if (items.length === 0) {
					warnings.push(m.inline_geo_empty_tag({ kind: 'geoip', tag }));
				} else {
					warnings.push(m.inline_geo_expanded({ kind: 'geoip', tag, count: items.length }));
					out.push(...items.map(formatIpCidrForList));
				}
			} catch (e) {
				warnings.push(`geoip:${tag}: ${(e as Error).message}`);
				out.push(rawLine);
			}
			continue;
		}

		out.push(rawLine);
	}

	return { text: out.join('\n'), warnings };
}

function stripInlineComment(s: string): string {
	return s
		.replace(/\s+;\s.*$/, '')
		.replace(/\s+#\s.*$/, '')
		.trim();
}
