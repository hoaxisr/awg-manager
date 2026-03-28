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
 * Determine search query type: 'ip', 'cidr', or 'domain'.
 */
export function detectQueryType(query: string): 'ip' | 'cidr' | 'domain' {
    if (isCIDR(query)) return 'cidr';
    if (isIPv4(query)) return 'ip';
    return 'domain';
}
