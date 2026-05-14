/**
 * IDE-style HTML underlay for ShareLinksTextarea: share schemes, JSON, YAML (incl. Clash).
 */

function escapeHtml(text: string): string {
	return text.replace(/&/g, '&amp;').replace(/</g, '&lt;').replace(/>/g, '&gt;');
}

const PROTO_CORE =
	'naive\\+https://|naive\\+http://|hysteria2://|vless://|hy2://|trojan://|ss://';

/** Match scheme at line start / after indent / after whitespace (not inside HTML). */
const PROTO_IN_ESCAPED = new RegExp(
	`(?:^|(?<=[\\n\\r])|(?<=[ \\t\\u00a0]))(${PROTO_CORE})`,
	'gi',
);

function wrapShareProtocolsInEscaped(escaped: string): string {
	return escaped.replace(
		PROTO_IN_ESCAPED,
		(_m, p1: string) => `<span class="share-link-proto">${p1}</span>`,
	);
}

function isShareLinkLine(line: string): boolean {
	const t = line.trimStart();
	return new RegExp(`^(?:${PROTO_CORE})`, 'i').test(t);
}

function highlightShareLinkLine(rawLine: string): string {
	const esc = escapeHtml(rawLine);
	return esc.replace(
		new RegExp(`(^[\\t ]*)(${PROTO_CORE})`, 'i'),
		(_, indent: string, proto: string) => `${indent}<span class="share-link-proto">${proto}</span>`,
	);
}

function firstNonEmptyTrimmedLine(raw: string): string {
	for (const line of raw.split('\n')) {
		const t = line.trim();
		if (t) return t;
	}
	return '';
}

/** First non-empty line starts with `{` or `[` — JSON-style coloring (works for invalid / partial JSON). */
function looksJsonStructure(raw: string): boolean {
	const t = firstNonEmptyTrimmedLine(raw);
	return t.startsWith('{') || t.startsWith('[');
}

function highlightJson(src: string): string {
	let i = 0;
	const n = src.length;
	let out = '';

	const punct = new Set('{}[],:');

	while (i < n) {
		const c = src[i];
		if (c === ' ' || c === '\n' || c === '\r' || c === '\t') {
			out += escapeHtml(c);
			i++;
			continue;
		}
		if (c === '"') {
			out += '<span class="hl-json-str">';
			out += '"';
			i++;
			let closed = false;
			while (i < n) {
				const d = src[i];
				if (d === '\\' && i + 1 < n) {
					out += escapeHtml(d) + escapeHtml(src[i + 1]);
					i += 2;
					continue;
				}
				if (d === '"') {
					out += '"</span>';
					i++;
					closed = true;
					break;
				}
				out += escapeHtml(d);
				i++;
			}
			if (!closed) {
				out += '</span>';
			}
			continue;
		}
		if (punct.has(c)) {
			out += `<span class="hl-json-punct">${escapeHtml(c)}</span>`;
			i++;
			continue;
		}
		if (c === '-' || (c >= '0' && c <= '9')) {
			let j = i;
			if (src[j] === '-') j++;
			while (j < n && /[0-9.eE+-]/.test(src[j] ?? '')) j++;
			if (j > i) {
				out += `<span class="hl-json-num">${escapeHtml(src.slice(i, j))}</span>`;
				i = j;
				continue;
			}
		}
		const rest = src.slice(i);
		if (/^null\b/.test(rest)) {
			out += '<span class="hl-json-lit">null</span>';
			i += 4;
			continue;
		}
		if (/^true\b/.test(rest)) {
			out += '<span class="hl-json-lit">true</span>';
			i += 4;
			continue;
		}
		if (/^false\b/.test(rest)) {
			out += '<span class="hl-json-lit">false</span>';
			i += 5;
			continue;
		}
		out += escapeHtml(c);
		i++;
	}
	return out;
}

/** YAML key:value with mandatory whitespace after ':' — avoids matching `vless://`. */
const YAML_KV = /^(\s*)([\w.-]+)(\s*:\s*)(.*)$/;

function highlightYamlTail(tail: string): string {
	const t = tail.trimStart();
	if (t.startsWith('"') || t.startsWith("'")) {
		const esc = escapeHtml(tail);
		return `<span class="hl-yaml-str">${esc}</span>`;
	}
	const esc = escapeHtml(tail);
	return wrapShareProtocolsInEscaped(esc);
}

function highlightYamlOrPlainLine(line: string): string {
	const trimmed = line.trimStart();
	if (trimmed.startsWith('#')) {
		return `<span class="hl-yaml-comment">${escapeHtml(line)}</span>`;
	}
	if (isShareLinkLine(line)) {
		return highlightShareLinkLine(line);
	}
	const kv = YAML_KV.exec(line);
	if (kv) {
		const [, indent, key, sep, tail] = kv;
		return `${escapeHtml(indent)}<span class="hl-yaml-key">${escapeHtml(key)}</span>${escapeHtml(sep)}${highlightYamlTail(tail)}`;
	}
	if (/^\s*-\s/.test(line)) {
		const m = /^(\s*-\s)(.*)$/.exec(line);
		if (m) {
			const [, dashPart, rest] = m;
			return `<span class="hl-yaml-punct">${escapeHtml(dashPart)}</span>${highlightYamlOrPlainLine(rest)}`;
		}
	}
	return wrapShareProtocolsInEscaped(escapeHtml(line));
}

function looksYamlishDocument(raw: string): boolean {
	const nonEmpty = raw
		.split('\n')
		.map((l) => l.trim())
		.filter(Boolean);
	if (nonEmpty.length === 0) return false;
	if (nonEmpty.every((l) => isShareLinkLine(l))) return false;
	const yamlish = nonEmpty.some(
		(l) =>
			l.startsWith('#') ||
			l.startsWith('-') ||
			l.startsWith('---') ||
			(YAML_KV.test(l) && !isShareLinkLine(l)),
	);
	return yamlish;
}

function highlightMixedYamlAndLinks(raw: string): string {
	const lines = raw.split('\n');
	const parts: string[] = [];
	for (let li = 0; li < lines.length; li++) {
		parts.push(highlightYamlOrPlainLine(lines[li]!));
		if (li < lines.length - 1) parts.push('\n');
	}
	return parts.join('');
}

/**
 * Produce HTML for the underlay: JSON, YAML/Clash, share links, or mixed YAML+links.
 */
export function highlightShareEditorContent(raw: string): string {
	if (!raw) return '';
	if (looksJsonStructure(raw)) {
		return highlightJson(raw);
	}
	if (looksYamlishDocument(raw)) {
		return highlightMixedYamlAndLinks(raw);
	}
	const esc = escapeHtml(raw);
	return wrapShareProtocolsInEscaped(esc);
}
