/**
 * Check if string is a valid IPv4 address.
 */
export function isIPv4(str: string): boolean {
    const parts = str.split('.');
    if (parts.length !== 4) return false;
    return parts.every(p => {
        const n = Number(p);
        return Number.isInteger(n) && n >= 0 && n <= 255 && p === String(n);
    });
}

/**
 * Check if string is a valid CIDR notation (e.g. "10.0.0.0/8").
 */
export function isCIDR(str: string): boolean {
    const slash = str.indexOf('/');
    if (slash === -1) return false;
    const ip = str.substring(0, slash);
    const prefix = Number(str.substring(slash + 1));
    return isIPv4(ip) && Number.isInteger(prefix) && prefix >= 0 && prefix <= 32;
}

/**
 * Parse IPv4 string to 32-bit number.
 */
function ipToNumber(ip: string): number {
    const parts = ip.split('.');
    return ((Number(parts[0]) << 24) | (Number(parts[1]) << 16) | (Number(parts[2]) << 8) | Number(parts[3])) >>> 0;
}

/**
 * Check if an IPv4 address falls within a CIDR range.
 * Example: ipInCIDR("10.0.0.5", "10.0.0.0/24") -> true
 */
export function ipInCIDR(ip: string, cidr: string): boolean {
    const slash = cidr.indexOf('/');
    if (slash === -1) return false;
    const networkIp = cidr.substring(0, slash);
    const prefix = Number(cidr.substring(slash + 1));
    if (prefix === 0) return true;
    const mask = (~0 << (32 - prefix)) >>> 0;
    return (ipToNumber(ip) & mask) === (ipToNumber(networkIp) & mask);
}

/**
 * Check whether two IPv4 CIDR ranges overlap — either intersect or one
 * contains the other. Returns false if either input is invalid.
 *
 * Example: cidrOverlaps("10.0.0.0/16", "10.0.0.0/8")  -> true  (subset)
 *          cidrOverlaps("10.0.0.0/24", "10.0.1.0/24") -> false (disjoint)
 */
export function cidrOverlaps(a: string, b: string): boolean {
    const pa = parseCIDR(a);
    const pb = parseCIDR(b);
    if (!pa || !pb) return false;
    // For IPv4, two CIDRs overlap iff one is a subset of the other —
    // compare their network addresses under the broader (shorter) mask.
    const minMask = pa.prefix < pb.prefix ? pa.mask : pb.mask;
    return ((pa.net & minMask) >>> 0) === ((pb.net & minMask) >>> 0);
}

function parseCIDR(cidr: string): { net: number; mask: number; prefix: number } | null {
    const slash = cidr.indexOf('/');
    if (slash === -1) return null;
    const ip = cidr.substring(0, slash);
    const prefixStr = cidr.substring(slash + 1);
    const prefix = Number(prefixStr);
    if (!isIPv4(ip) || !Number.isInteger(prefix) || prefix < 0 || prefix > 32) return null;
    const mask = prefix === 0 ? 0 : ((~0 << (32 - prefix)) >>> 0);
    const net = (ipToNumber(ip) & mask) >>> 0;
    return { net, mask, prefix };
}

/**
 * Determine search query type: 'ip', 'cidr', or 'domain'.
 */
export function detectQueryType(query: string): 'ip' | 'cidr' | 'domain' {
    if (isCIDR(query)) return 'cidr';
    if (isIPv4(query)) return 'ip';
    return 'domain';
}

/**
 * Parse "1.2.3.4/32 !ASTelegram" into { cidr, comment }.
 * If no "!" present, comment is empty string.
 */
export function parseSubnetComment(s: string): { cidr: string; comment: string } {
    const idx = s.indexOf('!');
    if (idx === -1) return { cidr: s.trim(), comment: '' };
    return {
        cidr: s.substring(0, idx).trim(),
        comment: s.substring(idx + 1).trim()
    };
}
