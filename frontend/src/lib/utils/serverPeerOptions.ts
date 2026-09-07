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

/** Поднятый WG-сервер, на который смотрит `-connect` freeturn-сервера, — по listen-порту. */
export interface ConnectServer {
	kind: ServerPeerKind;
	serverId: string;
	address: string;
	/** Адреса заведённых пиров без маски — для подбора следующего свободного. */
	peerIPs: string[];
}

export const hostIP = (raw: string) => raw.replace(/\/\d+$/, '');

export function findServerByListenPort(
	snap: ServersSnapshot | null,
	port: number,
): ConnectServer | null {
	if (!snap || !port) return null;
	// Статус `up` — как у списка пиров (buildRunningServerPeerDropdownOptions):
	// пир на остановленном сервере абоненту бесполезен.
	const m = snap.managed?.find(
		(s) => s.listenPort === port && snap.managedStats?.[s.interfaceName]?.status === 'up',
	);
	if (m) {
		return {
			kind: 'managed',
			serverId: m.interfaceName,
			address: hostIP(m.address),
			peerIPs: (m.peers ?? []).map((p) => hostIP(p.tunnelIP)),
		};
	}
	const s = snap.servers?.find((srv) => srv.listenPort === port && srv.status === 'up');
	if (!s) return null;
	return {
		kind: 'system',
		serverId: s.id,
		address: hostIP(s.address),
		peerIPs: (s.peers ?? []).flatMap((p) => (p.allowedIPs ?? []).map(hostIP)),
	};
}

/** Поднятые WG-серверы для выбора `-connect` раздачи (#871); значение — kind\0serverId. */
export function buildRunningServerDropdownOptions(snap: ServersSnapshot | null): DropdownOption[] {
	if (!snap) return [];
	const opts: DropdownOption[] = [];
	for (const s of snap.managed ?? []) {
		if (snap.managedStats?.[s.interfaceName]?.status !== 'up') continue;
		opts.push({
			value: encodeServerPeerValue('managed', s.interfaceName, ''),
			label: `Managed · ${s.description || s.interfaceName}`,
		});
	}
	for (const s of snap.servers ?? []) {
		if (s.status !== 'up') continue;
		opts.push({
			value: encodeServerPeerValue('system', s.id, ''),
			label: `Системный WG · ${s.description || s.interfaceName}`,
		});
	}
	return opts;
}

/** Значение опции сервера, на который смотрит `-connect`; пусто — не поднят. */
export function serverValueForConnect(snap: ServersSnapshot | null, connect: string): string {
	const own = findServerByListenPort(snap, parseLocalListenPort(connect) ?? 0);
	return own ? encodeServerPeerValue(own.kind, own.serverId, '') : '';
}

/** `-connect` для выбранного сервера: `127.0.0.1:<listenPort>`; пусто — порт неизвестен. */
export function connectForServerValue(snap: ServersSnapshot | null, value: string): string {
	if (!value) return '';
	const { kind, serverId } = decodeServerPeerValue(value);
	const port = resolveServerListenPort(snap, kind, serverId);
	return port ? `127.0.0.1:${port}` : '';
}

/** Следующий свободный /32 в /24 сервера. */
export function suggestNextPeerIP(address: string, usedIPs: string[]): string {
	const parts = address.split('.');
	if (parts.length !== 4) return '';
	const base = parts.slice(0, 3).join('.');
	const used = new Set([address, ...usedIPs]);
	for (let i = 2; i < 255; i++) {
		const candidate = `${base}.${i}`;
		if (!used.has(candidate)) return `${candidate}/32`;
	}
	return '';
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

/** True when a proxy link carries a usable WG client config (not empty stub). */
export function linkHasBundledWg(wg?: string | null): boolean {
	const raw = wg?.trim() ?? '';
	if (!raw) return false;
	return /PrivateKey\s*=/i.test(raw);
}

/** @deprecated use linkHasBundledWg */
export const freeturnLinkHasWg = linkHasBundledWg;

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
	let peerHeader = -1;
	const out = lines.map((line, i) => {
		const t = line.trim();
		if (t.startsWith('[')) {
			inPeer = t.toLowerCase() === '[peer]';
			if (inPeer && peerHeader < 0) peerHeader = i;
			return line;
		}
		if (inPeer && /^endpoint\s*=/i.test(t)) {
			replaced = true;
			return `Endpoint = ${host}:${port}`;
		}
		return line;
	});
	if (!replaced && conf.trim()) {
		// Строки Endpoint нет: вставить в существующую секцию [Peer], а не
		// открывать вторую — второй заголовок без PublicKey ломает конфиг.
		if (peerHeader >= 0) out.splice(peerHeader + 1, 0, `Endpoint = ${host}:${port}`);
		else out.push('', '[Peer]', `Endpoint = ${host}:${port}`);
	}
	return out.join('\n');
}
