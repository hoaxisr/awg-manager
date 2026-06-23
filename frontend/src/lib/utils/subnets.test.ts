import { describe, it, expect } from 'vitest';
import { normalizeSubnet, parseSubnets, serializeSubnets } from './subnets';

describe('normalizeSubnet', () => {
  it('keeps valid CIDR', () => expect(normalizeSubnet('203.0.113.0/24')).toBe('203.0.113.0/24'));
  it('promotes bare IP to /32', () => expect(normalizeSubnet('10.8.0.5')).toBe('10.8.0.5/32'));
  it('rejects hostname', () => expect(normalizeSubnet('vpn.example.com')).toBeNull());
  it('rejects garbage', () => expect(normalizeSubnet('nope')).toBeNull());
});

describe('parseSubnets', () => {
  it('splits comma+space and dedups', () =>
    expect(parseSubnets('203.0.113.0/24, 10.8.0.5 10.8.0.5')).toEqual(['203.0.113.0/24', '10.8.0.5/32']));
  it('drops invalid silently', () =>
    expect(parseSubnets('10.0.0.0/8, bad')).toEqual(['10.0.0.0/8']));
  it('empty → []', () => expect(parseSubnets('')).toEqual([]));
});

describe('serializeSubnets', () => {
  it('joins with comma-space', () =>
    expect(serializeSubnets(['10.0.0.0/8', '1.2.3.4/32'])).toBe('10.0.0.0/8, 1.2.3.4/32'));
});
