import type { SingboxRouterDNSServer, SingboxRouterDNSRule } from '$lib/types';
import { formatUsageBlockReason } from './outboundUsage';

export type DnsServerUsageInput = {
	tag: string;
	rules: readonly SingboxRouterDNSRule[];
	servers: readonly SingboxRouterDNSServer[];
	dnsFinal: string;
};

/** Один проход по DNS-конфигу: ссылки для всех интересующих тегов сразу. */
function collectAllDnsServerReferences(
	tags: readonly string[],
	input: Omit<DnsServerUsageInput, 'tag'>,
): Map<string, string[]> {
	const refs = new Map<string, string[]>(tags.map((t) => [t, []]));
	const push = (tag: string | undefined, ref: string) => {
		if (tag) refs.get(tag)?.push(ref);
	};

	input.rules.forEach((r, i) => {
		push(r.server, `rule[${i}]`);
	});
	for (const s of input.servers) {
		if (s.domain_resolver?.server && s.domain_resolver.server !== s.tag) {
			push(s.domain_resolver.server, `server[${s.tag}].domain_resolver`);
		}
	}
	push(input.dnsFinal, 'final');

	return refs;
}

/** Mirrors backend dnsServerReferences for UI delete guards. */
export function collectDnsServerReferences(input: DnsServerUsageInput): string[] {
	return collectAllDnsServerReferences([input.tag], input).get(input.tag) ?? [];
}

export function dnsServerDeleteBlockReason(
	server: SingboxRouterDNSServer,
	input: Omit<DnsServerUsageInput, 'tag'>,
): string | null {
	return dnsServerDeleteBlockReasons([server], input).get(server.tag) ?? null;
}

/** Причины блокировки удаления для всего списка за один проход. */
export function dnsServerDeleteBlockReasons(
	servers: readonly SingboxRouterDNSServer[],
	input: Omit<DnsServerUsageInput, 'tag'>,
): Map<string, string | null> {
	const refs = collectAllDnsServerReferences(servers.map((s) => s.tag), input);
	const reasons = new Map<string, string | null>();
	for (const s of servers) {
		reasons.set(s.tag, formatUsageBlockReason('DNS-сервер', refs.get(s.tag) ?? []));
	}
	return reasons;
}
