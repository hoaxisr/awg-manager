import { describe, it, expect } from 'vitest';
import {
  encodeServerPeerValue,
  decodeServerPeerValue,
  buildServerPeerDropdownOptions,
  parseLocalListenPort,
  patchWgConfEndpoint,
} from './serverPeerOptions';
import type { ServersSnapshot } from '$lib/stores/servers';
import type { ManagedServer, WireguardServer } from '$lib/types';

function snap(over: Partial<ServersSnapshot> = {}): ServersSnapshot {
  return { servers: [], managed: [], managedStats: {}, ...over };
}

// Фикстуры покрывают только поля, которые читает buildServerPeerDropdownOptions
// (id/interfaceName/description/peers); один явный каст в фабрике вместо
// рассыпанных as any.
function sysServer(p: {
  id: string;
  interfaceName: string;
  description: string;
  peers: Array<{ publicKey: string; description: string; confAvailable?: boolean }>;
}): WireguardServer {
  return p as unknown as WireguardServer;
}

function mngServer(p: {
  interfaceName: string;
  description: string;
  peers: Array<{ publicKey: string; description: string }>;
}): ManagedServer {
  return p as unknown as ManagedServer;
}

describe('serverPeerOptions value codec', () => {
  it('round-trips kind/serverId/pubkey', () => {
    const v = encodeServerPeerValue('system', 'wg-id-1', 'PUBKEYAAA');
    expect(decodeServerPeerValue(v)).toEqual({
      kind: 'system',
      serverId: 'wg-id-1',
      pubkey: 'PUBKEYAAA',
    });
  });
});

describe('buildServerPeerDropdownOptions', () => {
  it('null/empty snapshot → empty list', () => {
    expect(buildServerPeerDropdownOptions(null)).toEqual([]);
    expect(buildServerPeerDropdownOptions(snap())).toEqual([]);
  });

  it('filters system peers by confAvailable, keeps managed peers always', () => {
    const s = snap({
      servers: [
        sysServer({
          id: 'sys1',
          interfaceName: 'Wireguard0',
          description: 'Sys',
          peers: [
            { publicKey: 'SYS_OK', description: 'ok', confAvailable: true },
            { publicKey: 'SYS_NO', description: 'no', confAvailable: false },
            { publicKey: 'SYS_UNDEF', description: 'undef' },
          ],
        }),
      ],
      managed: [
        mngServer({
          interfaceName: 'awg-mng0',
          description: 'Mng',
          peers: [{ publicKey: 'MNG1', description: 'm1' }],
        }),
      ],
    });

    const opts = buildServerPeerDropdownOptions(s);
    const values = opts.map((o) => o.value);

    // system: only confAvailable === true
    expect(values).toContain(encodeServerPeerValue('system', 'sys1', 'SYS_OK'));
    expect(values).not.toContain(encodeServerPeerValue('system', 'sys1', 'SYS_NO'));
    expect(values).not.toContain(encodeServerPeerValue('system', 'sys1', 'SYS_UNDEF'));
    // managed: always (serverId = interfaceName)
    expect(values).toContain(encodeServerPeerValue('managed', 'awg-mng0', 'MNG1'));
  });

  it('pins per-kind serverId source: system uses id, not interfaceName', () => {
    // The documented 404-blocker: system must encode WireguardServer.id
    // ('sys1'), NOT interfaceName ('Wireguard0'). Fixture gives distinct
    // values so a swap is caught here, not only at runtime.
    const s = snap({
      servers: [
        sysServer({
          id: 'sys1',
          interfaceName: 'Wireguard0',
          description: 'Sys',
          peers: [{ publicKey: 'SYS_OK', description: 'ok', confAvailable: true }],
        }),
      ],
    });
    const opt = buildServerPeerDropdownOptions(s)[0];
    expect(decodeServerPeerValue(opt.value).serverId).toBe('sys1');
  });
});

describe('parseLocalListenPort', () => {
  it('parses host:port and bare port', () => {
    expect(parseLocalListenPort('127.0.0.1:9001')).toBe(9001);
    expect(parseLocalListenPort('9002')).toBe(9002);
    expect(parseLocalListenPort('')).toBeNull();
    expect(parseLocalListenPort('bad')).toBeNull();
  });
});

describe('patchWgConfEndpoint', () => {
  it('replaces [Peer] Endpoint with local listen port', () => {
    const conf = `[Interface]
PrivateKey = x

[Peer]
PublicKey = y
Endpoint = 127.0.0.1:9000
AllowedIPs = 0.0.0.0/0`;
    expect(patchWgConfEndpoint(conf, 9001)).toContain('Endpoint = 127.0.0.1:9001');
    expect(patchWgConfEndpoint(conf, 9001)).not.toContain(':9000');
  });
});
