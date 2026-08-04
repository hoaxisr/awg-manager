import type { DropdownOption } from '$lib/components/ui';
import type { ServersSnapshot } from '$lib/stores/servers';

export type ServerPeerKind = 'managed' | 'system';

const SEP = '\0';

export function encodeServerPeerValue(
  kind: ServerPeerKind,
  serverId: string,
  pubkey: string,
): string {
  return `${kind}${SEP}${serverId}${SEP}${pubkey}`;
}

export function decodeServerPeerValue(value: string): {
  kind: ServerPeerKind;
  serverId: string;
  pubkey: string;
} {
  const [kind, serverId, pubkey] = value.split(SEP);
  return { kind: kind as ServerPeerKind, serverId, pubkey };
}

function peerLabel(pubkey: string, description: string): string {
  return description || `${pubkey.slice(0, 16)}…`;
}

/**
 * Сгруппированные dropdown-опции «сервер → пир» для анализатора.
 * serverId per-kind: managed = interfaceName, system = id.
 * Системные пиры включаются только при confAvailable === true (де-факто
 * «создан средствами awgm»); managed-пиры — всегда.
 */
export function buildServerPeerDropdownOptions(
  snap: ServersSnapshot | null,
): DropdownOption[] {
  if (!snap) return [];
  const opts: DropdownOption[] = [];

  for (const s of snap.managed ?? []) {
    const group = `Managed · ${s.description || s.interfaceName}`;
    for (const p of s.peers ?? []) {
      opts.push({
        value: encodeServerPeerValue('managed', s.interfaceName, p.publicKey),
        label: peerLabel(p.publicKey, p.description),
        group,
      });
    }
  }

  for (const s of snap.servers ?? []) {
    const group = `Системный WG · ${s.description || s.interfaceName}`;
    for (const p of s.peers ?? []) {
      if (p.confAvailable !== true) continue;
      opts.push({
        value: encodeServerPeerValue('system', s.id, p.publicKey),
        label: peerLabel(p.publicKey, p.description),
        group,
      });
    }
  }

	return opts;
}

/** WG-серверы со status=up (managedStats или servers). */
export function buildRunningServerPeerDropdownOptions(
	snap: ServersSnapshot | null,
): DropdownOption[] {
	if (!snap) return [];
	const opts: DropdownOption[] = [];

	for (const s of snap.managed ?? []) {
		const st = snap.managedStats?.[s.interfaceName]?.status;
		if (st !== 'up') continue;
		const group = `Managed · ${s.description || s.interfaceName}`;
		for (const p of s.peers ?? []) {
			opts.push({
				value: encodeServerPeerValue('managed', s.interfaceName, p.publicKey),
				label: peerLabel(p.publicKey, p.description),
				group,
			});
		}
	}

	for (const s of snap.servers ?? []) {
		if (s.status !== 'up') continue;
		const group = `Системный WG · ${s.description || s.interfaceName}`;
		for (const p of s.peers ?? []) {
			const keeneticOnly = p.confAvailable !== true;
			opts.push({
				value: encodeServerPeerValue('system', s.id, p.publicKey),
				label: keeneticOnly
					? `${peerLabel(p.publicKey, p.description)} · Keenetic OS`
					: peerLabel(p.publicKey, p.description),
				group,
			});
		}
	}

	return opts;
}

export function resolveServerListenPort(
	snap: ServersSnapshot | null,
	kind: ServerPeerKind,
	serverId: string,
): number | null {
	if (!snap) return null;
	if (kind === 'managed') {
		const s = snap.managed?.find((m) => m.interfaceName === serverId);
		return s?.listenPort ?? null;
	}
	const s = snap.servers?.find((srv) => srv.id === serverId);
	return s?.listenPort ?? null;
}

export function wgEndpointHint(localPort: number): string {
	return `127.0.0.1:${localPort}`;
}

/** Порт из freeturn -listen (127.0.0.1:9001 → 9001). */
export function parseLocalListenPort(listen: string | undefined | null): number | null {
	const raw = listen?.trim();
	if (!raw) return null;
	const m = raw.match(/:(\d+)$/);
	if (m) {
		const port = Number(m[1]);
		return port > 0 && port <= 65535 ? port : null;
	}
	const port = Number(raw);
	return Number.isInteger(port) && port > 0 && port <= 65535 ? port : null;
}

/** True when freeturn:// carries a usable WG client config (not empty stub). */
export function freeturnLinkHasWg(wg?: string | null): boolean {
	const raw = wg?.trim() ?? '';
	if (!raw) return false;
	return /PrivateKey\s*=/i.test(raw);
}

/** Endpoint port for AWG tunnel linked to a proxy client: saved listen wins over link template. */
export function linkedTunnelListenPort(
	clientListen: string | undefined | null,
	payloadListen?: string | undefined | null
): number | null {
	return parseLocalListenPort(clientListen) ?? parseLocalListenPort(payloadListen);
}

/** Подставляет локальный Endpoint в [Peer] секцию конфига клиента. */
export function patchWgConfEndpoint(conf: string, localPort: number): string {
	const host = '127.0.0.1';
	const port = String(localPort);
	const lines = conf.split('\n');
	let inPeer = false;
	let replaced = false;
	const out = lines.map((line) => {
		const t = line.trim();
		if (t.startsWith('[')) {
			inPeer = t.toLowerCase() === '[peer]';
			return line;
		}
		if (inPeer && /^endpoint\s*=/i.test(t)) {
			replaced = true;
			return `Endpoint = ${host}:${port}`;
		}
		return line;
	});
	if (!replaced && conf.trim()) {
		out.push('', '[Peer]', `Endpoint = ${host}:${port}`);
	}
	return out.join('\n');
}
