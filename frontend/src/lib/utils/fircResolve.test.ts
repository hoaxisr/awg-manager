import { describe, expect, it } from 'vitest';
import { m } from '$lib/i18n';
import type { FircGroup, FircInterfaceOption } from '$lib/types/firc';
import { applyBulkGroupAction } from './fircBulkRules';
import { getEffectiveInterfaceProbe, isSelectableFircFallbackInterface, isSpecialFircIface } from './fircFallback';
import {
	applyProxyGroupToGroup,
	buildAvailableInterfaces,
	buildBulkInterfaces,
	buildCatalogTunnelOptions,
	formatIfaceButtonText,
	getPickerGroups,
	groupFormFrom,
	groupFromForm,
	groupStatusTooltip,
	normalizeGroup,
	resolverBadgeText,
	resolverBadgeTitle,
	setPrimaryIface,
	toggleBackupIface,
	type FircGroupsRuntime,
} from './fircGroups';
import {
	DOH_PRESETS,
	dnsOnlyBlocker,
	groupResolveError,
	isDuplicateServer,
	serverError,
	shortServer,
	splitServers,
} from './fircResolve';

const DOH = 'https://9.9.9.9/dns-query';
const OLD: FircInterfaceOption[] = [{ id: 'block' }, { id: 'nwg0', role: 'tunnel' }];
const NEW: FircInterfaceOption[] = [{ id: 'block' }, { id: 'dns' }, { id: 'nwg0', role: 'tunnel' }];

function group(over: Partial<FircGroup> = {}): FircGroup {
	return normalizeGroup({ id: 'g1', name: 'G', interface: 'nwg0', enable: true, rules: [], ...over });
}

const dnsGroup = (over: Partial<FircGroup> = {}) =>
	group({ interface: 'dns', resolve: { server: DOH, fallback: false }, ...over });

const subnetRule = { id: 'r1', type: 'subnet' as const, rule: '10.0.0.0/8', enable: true };

function runtime(interfaces = NEW): FircGroupsRuntime {
	return {
		groups: [], liveMap: {}, tunnelsState: [], interfaces, resolvers: [], proxyGroups: [],
		ifaceProbes: {}, tunnelProbes: {},
	};
}

describe('serverError', () => {
	// Случаи — из test_resolver_addr.c демона.
	it.each([
		'1.1.1.1', '1.1.1.1:5353', '2620:fe::fe', '[2620:fe::fe]', '[2620:fe::fe]:853',
		'https://1.1.1.1/', 'HTTPS://dns.example:8443/dns-query', 'https://[2620:fe::fe]/', 'https://128.0.0.1/q',
		`https://h.example/${'p'.repeat(255 - 'https://h.example/'.length)}`,
		...DOH_PRESETS.map((p) => p.url),
	])('accepts %s', (s) => expect(serverError(s)).toBe(''));

	it.each([
		['dns.example', m.firc_resolve_err_addr()],
		['1.1.1.1:', m.firc_resolve_err_addr()],
		['[1.1.1.1]', m.firc_resolve_err_addr()],
		['[2620:fe::fe', m.firc_resolve_err_addr()],
		['fe80::1%eth0', m.firc_resolve_err_addr()],
		['1.1.1.1:0', m.firc_resolve_err_port()],
		['1.1.1.1:65536', m.firc_resolve_err_port()],
		['1.1.1.1:000053', m.firc_resolve_err_port()],
		['127.0.0.1', m.firc_resolve_err_sink()],
		['0.0.0.0', m.firc_resolve_err_sink()],
		['::1', m.firc_resolve_err_sink()],
		['::ffff:9.9.9.9', m.firc_resolve_err_sink()],
		['http://dns.example/dns-query', m.firc_resolve_err_url()],
		['https:///dns-query', m.firc_resolve_err_url()],
		['https://:443/dns-query', m.firc_resolve_err_url()],
		['https://dns.example', m.firc_resolve_err_url()],
		['https://user@dns.example/dns-query', m.firc_resolve_err_url()],
		['https://dns.example/dns-query?x=1', m.firc_resolve_err_url()],
		['https://dns.example/dns-query#f', m.firc_resolve_err_url()],
		['https://dns.example/dns query', m.firc_resolve_err_url()],
		['https://dns.example:0/x', m.firc_resolve_err_port()],
		['https://dns.example:65536/x', m.firc_resolve_err_port()],
		['https://dns.example:44x/x', m.firc_resolve_err_port()],
		['https://2620:fe::fe/x', m.firc_resolve_err_url()],
		['https://[2606:4700::1111/x', m.firc_resolve_err_url()],
		['https://[]/x', m.firc_resolve_err_url()],
		['https://[1.1.1.1]/x', m.firc_resolve_err_url()],
		['https://127.9.9.9:8443/q', m.firc_resolve_err_sink()],
		['https://[::1]/q', m.firc_resolve_err_sink()],
		[`https://h.example/${'p'.repeat(256 - 'https://h.example/'.length)}`, m.firc_resolve_err_url_long()],
	])('rejects %s', (s, want) => expect(serverError(s)).toBe(want));

	it('refuses DoH when the daemon does not understand it', () => {
		expect(serverError(DOH, false)).toBe(m.firc_resolve_err_doh_unsupported());
		expect(serverError('1.1.1.1', false)).toBe('');
	});
});

describe('groupResolveError', () => {
	it('checks the list as a whole', () => {
		const of = (server: string) => groupResolveError(group({ resolve: { tunnel: true, server } }));
		expect(of('')).toBe('');
		expect(of(`1.1.1.1, ${DOH}`)).toBe('');
		expect(of('1.1.1.1,2.2.2.2,3.3.3.3,4.4.4.4,5.5.5.5')).toBe(m.firc_resolve_err_too_many({ max: 4 }));
		expect(of('1.1.1.1:53, 1.1.1.1')).toContain(m.firc_resolve_err_duplicate());
		expect(of('https://dns.example')).toContain(m.firc_resolve_err_url());
		expect(isDuplicateServer(['1.1.1.1', '', '1.1.1.1:53'], 2)).toBe(true);
		expect(isDuplicateServer(['https://a/d', 'https://a/D'], 1)).toBe(false);
	});

	it('requires servers and refuses subnet rules on a dns group', () => {
		expect(groupResolveError(dnsGroup())).toBe('');
		expect(groupResolveError(dnsGroup({ resolve: { server: '' } }))).toBe(m.firc_dns_only_server_required());
		expect(groupResolveError(dnsGroup({ rules: [subnetRule] }))).toBe(m.firc_dns_only_no_subnets());
		expect(groupResolveError(group({ rules: [subnetRule] }))).toBe('');
	});
});

describe('dns groups in the panel', () => {
	it('keeps fallback and drops what a dns group has not', () => {
		expect(dnsGroup().resolve).toEqual({ server: DOH, fallback: false });
		expect(dnsGroup({ resolve: { server: DOH } }).resolve).toEqual({ server: DOH, fallback: true });
		// Обычная группа не несёт fallback: старому демону уходит прежняя форма.
		expect(group({ resolve: { tunnel: false, server: '', fallback: true } }).resolve).toEqual({ tunnel: false, server: '' });
		expect(group().resolve).toEqual({ tunnel: true, server: '' });
	});

	it('refits the group when its outbound changes', () => {
		const g = group({
			interface: ['nwg0', 'eth3'], cloudflare: true, check: { mode: 'url', url: 'https://a.example' },
			resolve: { tunnel: false, server: DOH },
		});
		expect(setPrimaryIface(g, 'dns')).toBe(true);
		expect(g.interface).toBe('dns');
		expect(g.resolve).toEqual({ server: DOH, fallback: true });
		expect(g.cloudflare).toBeUndefined();
		expect(g.check?.mode).toBe('none');

		toggleBackupIface(g, 'dns');
		expect(g.interface).toBe('dns');

		const back = dnsGroup();
		toggleBackupIface(back, 'nwg0');
		expect(back.interface).toBe('nwg0');
		expect(back.resolve).toEqual({ tunnel: true, server: DOH });

		const viaPg = dnsGroup();
		applyProxyGroupToGroup(viaPg, { id: 'pg', name: 'PG', interfaces: ['nwg0', 'eth3'] });
		expect(viaPg.resolve).toEqual({ tunnel: true, server: DOH });

		const bulk = (iface: string, g: FircGroup) =>
			applyBulkGroupAction([g], new Set(['g1']), { kind: 'setInterface', iface }).groups[0].resolve;
		expect(bulk('nwg0', dnsGroup())).toEqual({ tunnel: true, server: DOH });
		expect(bulk('dns', group({ resolve: { tunnel: true, server: DOH } }))).toEqual({ server: DOH, fallback: true });
	});

	it('round-trips through the settings form', () => {
		const form = groupFormFrom(dnsGroup({ resolve: { server: `1.1.1.1, ${DOH}`, fallback: false } }));
		expect(form).toMatchObject({ iface: 'dns', dnsServers: ['1.1.1.1', DOH], dnsFallback: false });
		expect(groupFromForm(form, dnsGroup(), 1).resolve).toEqual({ server: `1.1.1.1, ${DOH}`, fallback: false });

		// Со сменой выхода запасной DNS группе больше не положен.
		expect(groupFromForm({ ...form, iface: 'nwg0' }, dnsGroup(), 1).resolve).toEqual({ tunnel: true, server: `1.1.1.1, ${DOH}` });

		const plain = groupFormFrom(group());
		expect(plain).toMatchObject({ dnsMode: 'auto', dnsServers: [''] });
		const created = groupFromForm({ ...plain, iface: 'dns', dnsServers: [DOH, ' '], dnsFallback: false }, undefined, 0);
		expect(created.resolve).toEqual({ server: DOH, fallback: false });
		expect(groupFromForm({ ...plain, dnsMode: 'off', dnsServers: [DOH] }, group(), 1).resolve).toEqual({ tunnel: false, server: '' });
		const cloudflare = group({ cloudflare: true });
		expect(groupFromForm({ ...plain, iface: 'dns', dnsServers: [DOH] }, cloudflare, 1).cloudflare).toBeUndefined();
	});

	it('is offered as an outbound only with a daemon that knows it', () => {
		const values = (ifaces: FircInterfaceOption[], selected = '') =>
			buildAvailableInterfaces(ifaces, selected).filter((o) => o.value === 'dns').map((o) => o.label);
		expect(values(NEW)).toEqual([m.firc_dns_only()]);
		expect(values(OLD)).toEqual([]);
		expect(values(OLD, 'dns')).toEqual([m.firc_dns_only()]);
		expect(buildCatalogTunnelOptions(NEW).map((o) => o.value)).not.toContain('dns');
		expect(getPickerGroups(NEW, group()).others).toEqual([]);
		expect(getPickerGroups(NEW, dnsGroup()).tunnels.map((t) => t.id)).toEqual(['nwg0']);
	});

	it('explains why a group cannot become dns only', () => {
		const ready = group({ resolve: { tunnel: true, server: DOH } });
		expect(dnsOnlyBlocker(ready, NEW)).toBe('');
		expect(dnsOnlyBlocker(ready, OLD)).toBe(m.firc_dns_only_need_update());
		expect(dnsOnlyBlocker(group(), NEW)).toBe(m.firc_dns_only_need_server());
		expect(dnsOnlyBlocker({ ...ready, rules: [subnetRule] }, NEW)).toBe(m.firc_dns_only_no_subnets());
		expect(dnsOnlyBlocker(dnsGroup(), OLD)).toBe('');

		const bulk = (selected: FircGroup[], ifaces = NEW) =>
			buildBulkInterfaces(ifaces, selected).filter((o) => o.value === 'dns');
		expect(bulk([ready, dnsGroup()])).toEqual([{ value: 'dns', label: m.firc_dns_only(), disabled: false }]);
		expect(bulk([ready, group()])).toEqual([
			{ value: 'dns', label: m.firc_dns_only_blocked({ reason: m.firc_dns_only_need_server() }), disabled: true },
		]);
		expect(bulk([ready], OLD)[0].disabled).toBe(true);
	});

	it('never takes part in fallback chains and probes', () => {
		expect(isSpecialFircIface('dns')).toBe(true);
		expect(isSelectableFircFallbackInterface({ id: 'dns' })).toBe(false);
		expect(getEffectiveInterfaceProbe('dns', { dns: { interface: 'dns', ok: true, latencyMs: 1 } })).toBeNull();
	});

	it('shows readable labels', () => {
		const rt = runtime();
		expect(formatIfaceButtonText(rt, dnsGroup())).toBe(m.firc_dns_only());
		expect(groupStatusTooltip({ g1: { live: true } }, dnsGroup())).toBe(m.firc_dns_only_live());
		expect(shortServer('https://dns.example:8443/account-id/q')).toBe('dns.example:8443');
		expect(shortServer('1.1.1.1:53')).toBe('1.1.1.1:53');
		expect(splitServers(' 1.1.1.1 ,, 2.2.2.2')).toEqual(['1.1.1.1', '2.2.2.2']);
		const mixed = dnsGroup({ resolve: { server: `${DOH}, 1.1.1.1` } });
		expect(resolverBadgeText(rt, mixed)).toBe('DNS: 9.9.9.9, 1.1.1.1');
		expect(resolverBadgeTitle(rt, mixed)).toBe(`DNS: ${DOH}, 1.1.1.1`);
		expect(resolverBadgeText(rt, group({ resolve: { tunnel: false, server: '' } }))).toBe(m.firc_groups_dns_shared());
		const live = { ...rt, liveMap: { g1: { live: true, resolver: { source: 'group' as const, servers: [DOH] } } } };
		expect(resolverBadgeText(live, group())).toBe('DNS: 9.9.9.9');
	});
});
