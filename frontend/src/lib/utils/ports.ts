export interface PortEntry {
  port: number;
  proto: 'TCP' | 'UDP';
}

export type ParseEntryResult =
  | { ok: true; entry: PortEntry }
  | { ok: false; error: string };

// Forgiving single-entry parser: extract the first number (port) and a
// tcp/udp token (any case), separators irrelevant. Accepts "443 TCP",
// "tcp:993", "993/tcp", even "tcp25".
export function parsePortEntry(raw: string): ParseEntryResult {
  const s = raw.trim();
  const portMatch = s.match(/\d+/);
  if (!portMatch) return { ok: false, error: 'укажите порт' };
  const port = parseInt(portMatch[0], 10);
  if (port < 1 || port > 65535) return { ok: false, error: 'порт должен быть 1–65535' };
  const protoMatch = s.match(/tcp|udp/i);
  if (!protoMatch) return { ok: false, error: 'укажите протокол: TCP или UDP' };
  return { ok: true, entry: { port, proto: protoMatch[0].toUpperCase() as 'TCP' | 'UDP' } };
}

// Canonical dedup/identity key for a port entry. Single source so the parser
// and any UI keying stay in sync.
export function portKey(e: PortEntry): string {
  return `${e.port}/${e.proto}`;
}

// "443 TCP, 53 UDP" -> entries; invalid entries skipped, duplicates removed.
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

// Parse a user draft that may contain several comma-separated entries (e.g.
// pasted "443 tcp, 53 udp"). All-or-nothing: if any part is invalid, returns
// the first error and no entries — never a silent partial accept.
export function parseDraftEntries(raw: string):
  | { ok: true; entries: PortEntry[] }
  | { ok: false; error: string } {
  const parts = raw.split(',').map((p) => p.trim()).filter(Boolean);
  const entries: PortEntry[] = [];
  for (const part of parts) {
    const r = parsePortEntry(part);
    if (!r.ok) return { ok: false, error: r.error };
    entries.push(r.entry);
  }
  return { ok: true, entries };
}

// entries -> "443 TCP, 53 UDP" (backend parseExtraPorts grammar).
export function serializePorts(entries: PortEntry[]): string {
  return entries.map((e) => `${e.port} ${e.proto}`).join(', ');
}
