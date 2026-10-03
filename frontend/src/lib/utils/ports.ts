import { m } from '$lib/i18n';

export interface PortEntry {
  /** Start of the range (equals `to` for a single port). */
  from: number;
  /** End of the range (equals `from` for a single port). */
  to: number;
  proto: 'TCP' | 'UDP';
}

export type ParseEntryResult =
  | { ok: true; entry: PortEntry }
  | { ok: false; error: string };

/**
 * Forgiving single-entry parser.
 * Accepted forms (case-insensitive protocol, flexible separators):
 *   "443 TCP"          — single port
 *   "5000-5500 UDP"    — port range
 *   "tcp:993"          — alt separator
 *   "993/tcp"          — alt separator
 */
export function parsePortEntry(raw: string): ParseEntryResult {
  const s = raw.trim();

  // Extract protocol token first (tcp|udp, any case)
  const protoMatch = s.match(/tcp|udp/i);
  if (!protoMatch) return { ok: false, error: m.ports_need_protocol() };
  const proto = protoMatch[0].toUpperCase() as 'TCP' | 'UDP';

  // Extract numeric tokens — range "5000-5500" or single "443"
  const numTokens = s.match(/\d+/g);
  if (!numTokens || numTokens.length === 0) return { ok: false, error: m.ports_need_port() };

  // Determine whether this looks like a range: original string has "N-N" pattern
  // (ignoring any surrounding proto/separators)
  const rangeMatch = s.match(/(\d+)\s*-\s*(\d+)/);
  let from: number;
  let to: number;

  if (rangeMatch) {
    from = parseInt(rangeMatch[1], 10);
    to = parseInt(rangeMatch[2], 10);
  } else {
    from = parseInt(numTokens[0], 10);
    to = from;
  }

  if (from < 1 || from > 65535) return { ok: false, error: m.ports_out_of_range() };
  if (to < 1 || to > 65535) return { ok: false, error: m.ports_out_of_range() };
  if (from > to) return { ok: false, error: m.ports_range_order() };

  return { ok: true, entry: { from, to, proto } };
}

/** Canonical dedup/identity key for a port entry. */
export function portKey(e: PortEntry): string {
  return `${e.from}-${e.to}/${e.proto}`;
}

/** Human-readable label shown on the chip: "443/TCP" or "5000–5500/UDP". */
export function portLabel(e: PortEntry): string {
  if (e.from === e.to) return `${e.from}/${e.proto}`;
  return `${e.from}–${e.to}/${e.proto}`;
}

/**
 * Serialize to backend grammar: "PORT UDP|TCP" or "PORT-PORT UDP|TCP".
 * E.g. [{ from:443, to:443, proto:'TCP' }, { from:5000, to:5500, proto:'UDP' }]
 *   → "443 TCP, 5000-5500 UDP"
 */
export function serializePorts(entries: PortEntry[]): string {
  return entries
    .map((e) => (e.from === e.to ? `${e.from} ${e.proto}` : `${e.from}-${e.to} ${e.proto}`))
    .join(', ');
}

/**
 * "443 TCP, 5000-5500 UDP" → entries; invalid entries skipped, duplicates removed.
 * Handles both "PORT PROTO" and "PORT-PORT PROTO" formats.
 */
export function parsePortsString(s: string): PortEntry[] {
  const out: PortEntry[] = [];
  const seen = new Set<string>();
  for (const part of s.split(',')) {
    if (!part.trim()) continue;
    const r = parsePortEntry(part);
    if (!r.ok) continue;
    const key = portKey(r.entry);
    if (seen.has(key)) continue;
    seen.add(key);
    out.push(r.entry);
  }
  return out;
}

/**
 * Parse a user draft that may contain several comma-separated entries.
 * All-or-nothing: if any part is invalid, returns the first error.
 */
export function parseDraftEntries(raw: string):
  | { ok: true; entries: PortEntry[] }
  | { ok: false; error: string } {
  const parts = raw.split(',').map((p) => p.trim()).filter(Boolean);
  if (parts.length === 0) return { ok: true, entries: [] };
  const entries: PortEntry[] = [];
  for (const part of parts) {
    const r = parsePortEntry(part);
    if (!r.ok) return { ok: false, error: r.error };
    entries.push(r.entry);
  }
  return { ok: true, entries };
}
