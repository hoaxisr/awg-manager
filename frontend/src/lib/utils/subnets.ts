import { isCIDR, isIPv4 } from './cidr';

/** Голый IPv4 → "/32" (совпасть с backend resolveBypassSubnets); CIDR как есть; иначе null. */
export function normalizeSubnet(s: string): string | null {
  s = s.trim();
  if (isCIDR(s)) return s;
  if (isIPv4(s)) return s + '/32';
  return null;
}

/** Split по запятой/пробелу, normalize, dedup; невалидные молча отброшены. */
export function parseSubnets(s: string): string[] {
  const out: string[] = [];
  const seen = new Set<string>();
  for (const f of s.split(/[,\s]+/)) {
    const n = normalizeSubnet(f);
    if (n && !seen.has(n)) {
      seen.add(n);
      out.push(n);
    }
  }
  return out;
}

export function serializeSubnets(list: string[]): string {
  return list.join(', ');
}
