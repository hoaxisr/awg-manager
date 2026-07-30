import { describe, it, expect } from 'vitest';
import type { SingboxRouterDNSServer } from '$lib/types';
import {
  DNS_PRESETS,
  buildDnsServer,
  certPinWillReset,
  findDnsPresetByIp,
  protoOfDnsServer,
  udpDropsTls,
} from './dnsPresets';

const udpBase: SingboxRouterDNSServer = {
  tag: 'dns-direct',
  type: 'udp',
  server: '77.88.8.8',
};

const dotBase: SingboxRouterDNSServer = {
  tag: 'dns-direct',
  type: 'tls',
  server: '1.1.1.1',
  server_port: 853,
  domain_strategy: 'prefer_ipv4',
  domain_resolver: { server: 'dns-bootstrap', strategy: 'ipv4_only' },
  tls: {
    server_name: 'cloudflare-dns.com',
    alpn: ['h2'],
    min_version: '1.2',
    certificate_public_key_sha256: ['AAAA'],
  },
};

describe('DNS_PRESETS', () => {
  it('пять провайдеров, у каждого IPv4-адрес и SNI', () => {
    expect(DNS_PRESETS.map((p) => p.id)).toEqual([
      'yandex',
      'quad9',
      'cloudflare',
      'google',
      'adguard',
    ]);
    for (const p of DNS_PRESETS) {
      expect(p.ip).toMatch(/^\d+\.\d+\.\d+\.\d+$/);
      expect(p.sni).not.toBe('');
    }
  });

  it('фильтрующие пресеты подписаны', () => {
    expect(DNS_PRESETS.find((p) => p.id === 'quad9')?.note).toBe('блокирует malware');
    expect(DNS_PRESETS.find((p) => p.id === 'adguard')?.note).toBe('блокирует рекламу');
    expect(DNS_PRESETS.find((p) => p.id === 'cloudflare')?.note).toBeUndefined();
  });
});

describe('findDnsPresetByIp', () => {
  it('находит по точному адресу, игнорируя пробелы', () => {
    expect(findDnsPresetByIp(' 9.9.9.9 ')?.id).toBe('quad9');
  });

  it('чужой адрес — undefined', () => {
    expect(findDnsPresetByIp('192.168.1.1')).toBeUndefined();
  });
});

describe('protoOfDnsServer', () => {
  it('tls → dot, https → doh, остальное → udp', () => {
    expect(protoOfDnsServer({ type: 'tls' })).toBe('dot');
    expect(protoOfDnsServer({ type: 'https' })).toBe('doh');
    expect(protoOfDnsServer({ type: 'udp' })).toBe('udp');
    expect(protoOfDnsServer({ type: 'local' })).toBe('udp');
  });
});

describe('udpDropsTls', () => {
  it('true, когда есть содержательные TLS-настройки', () => {
    expect(udpDropsTls(dotBase)).toBe(true);
  });

  it('false, когда в tls только server_name — он всё равно перетирается', () => {
    expect(udpDropsTls({ ...udpBase, tls: { server_name: 'x.example' } })).toBe(false);
  });

  it('false без блока tls', () => {
    expect(udpDropsTls(udpBase)).toBe(false);
  });
});

describe('buildDnsServer', () => {
  it('udp: адрес без порта, пути и tls', () => {
    expect(buildDnsServer(udpBase, '8.8.8.8', 'dns.google', 'udp')).toEqual({
      tag: 'dns-direct',
      type: 'udp',
      server: '8.8.8.8',
    });
  });

  it('dot: type=tls, порт 853, SNI, без пути', () => {
    expect(buildDnsServer(udpBase, '9.9.9.9', 'dns.quad9.net', 'dot')).toEqual({
      tag: 'dns-direct',
      type: 'tls',
      server: '9.9.9.9',
      server_port: 853,
      tls: { server_name: 'dns.quad9.net' },
    });
  });

  it('doh: type=https, порт 443, путь /dns-query, SNI', () => {
    expect(buildDnsServer(udpBase, '1.1.1.1', 'cloudflare-dns.com', 'doh')).toEqual({
      tag: 'dns-direct',
      type: 'https',
      server: '1.1.1.1',
      server_port: 443,
      path: '/dns-query',
      tls: { server_name: 'cloudflare-dns.com' },
    });
  });

  it('DoH → UDP: не остаётся ни path, ни server_port, ни tls', () => {
    const doh = buildDnsServer(udpBase, '1.1.1.1', 'cloudflare-dns.com', 'doh');
    const back = buildDnsServer(doh, '8.8.8.8', 'dns.google', 'udp');
    expect(back.path).toBeUndefined();
    expect(back.server_port).toBeUndefined();
    expect(back.tls).toBeUndefined();
  });

  it('DoT → DoH со сменой адреса и протокола: пин и alpn сброшены, версии и domain_resolver переносятся', () => {
    const out = buildDnsServer(dotBase, '9.9.9.9', 'dns.quad9.net', 'doh');
    expect(out.tls).toEqual({
      server_name: 'dns.quad9.net',
      min_version: '1.2',
    });
    expect(out.domain_resolver).toEqual({ server: 'dns-bootstrap', strategy: 'ipv4_only' });
    expect(out.domain_strategy).toBe('prefer_ipv4');
  });

  it('смена адреса выбрасывает пин, но сохраняет insecure и версии TLS', () => {
    const base: SingboxRouterDNSServer = {
      ...dotBase,
      tls: { ...dotBase.tls, insecure: true },
    };
    const out = buildDnsServer(base, '9.9.9.9', 'dns.quad9.net', 'dot');
    expect(out.tls).toEqual({
      server_name: 'dns.quad9.net',
      alpn: ['h2'],
      insecure: true,
      min_version: '1.2',
    });
  });

  it('смена протокола выбрасывает alpn, адрес тот же — пин сохраняется', () => {
    const out = buildDnsServer(dotBase, '1.1.1.1', 'cloudflare-dns.com', 'doh');
    expect(out.tls).toEqual({
      server_name: 'cloudflare-dns.com',
      min_version: '1.2',
      certificate_public_key_sha256: ['AAAA'],
    });
  });

  it('тот же адрес и тот же протокол: пин и alpn сохраняются', () => {
    const out = buildDnsServer(dotBase, '1.1.1.1', 'cloudflare-dns.com', 'dot');
    expect(out.tls).toEqual({
      server_name: 'cloudflare-dns.com',
      alpn: ['h2'],
      min_version: '1.2',
      certificate_public_key_sha256: ['AAAA'],
    });
  });

  it('переносит detour и не создаёт пустых полей', () => {
    const tunnel: SingboxRouterDNSServer = {
      tag: 'dns-tunnel',
      type: 'udp',
      server: '9.9.9.9',
      detour: 'wg-nl',
    };
    const out = buildDnsServer(tunnel, '8.8.8.8', '', 'udp');
    expect(out).toEqual({ tag: 'dns-tunnel', type: 'udp', server: '8.8.8.8', detour: 'wg-nl' });
  });

  it('domain_resolver копируется, а не переиспользуется по ссылке', () => {
    const out = buildDnsServer(dotBase, '9.9.9.9', 'dns.quad9.net', 'doh');
    expect(out.domain_resolver).not.toBe(dotBase.domain_resolver);
  });
});

describe('certPinWillReset', () => {
  it('true при смене адреса, если пин был', () => {
    expect(certPinWillReset(dotBase, '9.9.9.9')).toBe(true);
  });

  it('false при том же адресе', () => {
    expect(certPinWillReset(dotBase, '1.1.1.1')).toBe(false);
  });

  it('false без пина в base', () => {
    expect(certPinWillReset({ ...dotBase, tls: { server_name: 'x' } }, '9.9.9.9')).toBe(false);
  });
});
