import { api } from '$lib/api/client';
import type { FircExtConfig, FircStatus } from '$lib/types/firc';

export type FircInboundServerId =
	| `managed:${string}`
	| `wg:${string}`
	| 'awg_server'
	| 'wdtt_server'
	| 'freeturn_server'
	| 'vless_xhttp_server'
	| 'openflux_server'
	| 'keenetic_wg'
	| 'keenetic_sstp';

export function extractFircEnabledServers(ext: FircExtConfig | null | undefined): string[] {
	if (!ext?.serverRouting) return [];
	if (Array.isArray(ext.serverRouting.enabledServers)) {
		return [...ext.serverRouting.enabledServers];
	}
	const list: string[] = [];
	if (ext.serverRouting.awgServer) list.push('awg_server');
	if (ext.serverRouting.wdttServer) list.push('wdtt_server');
	if (ext.serverRouting.freeTurnServer) list.push('freeturn_server');
	if (ext.serverRouting.vlessXhttpServer) list.push('vless_xhttp_server');
	return list;
}

export async function getFircEnabledServers(): Promise<string[] | null> {
	const fn = (api as { fircGetExt?: () => Promise<FircExtConfig> }).fircGetExt;
	if (typeof fn !== 'function') return null;
	try {
		const ext = await fn.call(api);
		return extractFircEnabledServers(ext);
	} catch {
		return null;
	}
}

export async function isFircServerRoutingEnabled(
	serverId: FircInboundServerId,
): Promise<boolean | null> {
	const list = await getFircEnabledServers();
	if (list === null) return null;
	return list.includes(serverId);
}

export async function setFircServerRoutingEnabled(
	serverId: FircInboundServerId,
	enabled: boolean,
	clearLegacyIds: string[] = [],
): Promise<FircExtConfig | null> {
	const getFn = (api as { fircGetExt?: () => Promise<FircExtConfig> }).fircGetExt;
	const saveFn = (api as { fircSaveExt?: (cfg: FircExtConfig) => Promise<FircExtConfig> }).fircSaveExt;
	if (typeof getFn !== 'function' || typeof saveFn !== 'function') return null;
	// При стороннем firc эта настройка не действует, а сохранение отклоняется.
	const statusFn = (api as { fircGetStatus?: () => Promise<FircStatus> }).fircGetStatus;
	if (typeof statusFn === 'function' && (await statusFn.call(api))?.foreign) return null;
	const ext = await getFn.call(api);
	const cur = extractFircEnabledServers(ext);
	const next = enabled
		? cur.includes(serverId)
			? cur
			: [...cur, serverId]
		: cur.filter((id) => id !== serverId && !clearLegacyIds.includes(id));
	const payload: FircExtConfig = {
		globalTarget: ext?.globalTarget,
		proxyGroups: ext?.proxyGroups ?? [],
		keenDnsCloud: ext?.keenDnsCloud ?? { enabled: false, proxyGroupId: '', interfaces: [] },
		serverRouting: {
			enabledServers: next,
			awgServer: next.includes('awg_server'),
			wdttServer: next.includes('wdtt_server'),
			freeTurnServer: next.includes('freeturn_server'),
			vlessXhttpServer: next.includes('vless_xhttp_server'),
		},
	};
	return await saveFn.call(api, payload);
}
