import type { SubscriptionMember } from '$lib/types';
import type { SubscriptionMemberSortKey } from '$lib/stores/tunnelTableSort';
import {
	applyDirection,
	compareBool,
	compareDelayLike,
	compareString,
} from '$lib/utils/tunnelTableSort';
import { singboxProtocolAliases, singboxProtocolLabel } from '$lib/utils/singboxProtocol';
import { latestPositiveDelay, matchQuery } from '$lib/components/tunnels/tunnelPageSelectors';

export interface ProtocolFilterOption {
	protocol: string;
	label: string;
	count: number;
}

/**
 * Extracts unique protocols present in the current subscription member list,
 * preserving display label and server counts.
 */
export function extractMemberProtocols(members: SubscriptionMember[]): ProtocolFilterOption[] {
	const counts = new Map<string, number>();
	for (const member of members) {
		const proto = member.protocol || '';
		if (!proto) continue;
		counts.set(proto, (counts.get(proto) ?? 0) + 1);
	}
	return [...counts.entries()]
		.map(([protocol, count]) => ({
			protocol,
			label: singboxProtocolLabel(protocol),
			count,
		}))
		.sort((a, b) => compareString(a.label, b.label));
}

/**
 * Returns true if a subscription member matches the search substring across
 * name, host/port, protocol (+ common aliases), transport, security, SNI and tag.
 */
export function memberMatchesSearch(member: SubscriptionMember, query: string): boolean {
	const trimmed = query.trim();
	if (!trimmed) return true;
	const hostPort = member.server && member.port ? `${member.server}:${member.port}` : '';
	return matchQuery(
		[
			member.label,
			member.server,
			member.port ? String(member.port) : '',
			hostPort,
			member.protocol,
			singboxProtocolLabel(member.protocol),
			...singboxProtocolAliases(member.protocol),
			member.transport,
			member.security,
			member.sni,
			member.tag,
		],
		trimmed,
	);
}

export interface MemberFilterSortOptions {
	searchQuery: string;
	excludedProtocols: Set<string>;
	sortBy: SubscriptionMemberSortKey | null;
	asc: boolean;
	activeMember: string | null;
	getDelayHistory: () => Map<string, number[]>;
}

/**
 * Filters and sorts subscription members without mutating the source array.
 */
export function filterAndSortMembers(
	members: SubscriptionMember[],
	options: MemberFilterSortOptions,
): SubscriptionMember[] {
	const { searchQuery, excludedProtocols, sortBy, asc, activeMember, getDelayHistory } = options;
	const filtered = members.filter((member) => {
		if (excludedProtocols.has(member.protocol)) return false;
		return memberMatchesSearch(member, searchQuery);
	});
	if (!sortBy) return filtered;

	const delayHistory = sortBy === 'delay' || sortBy === 'ping' ? getDelayHistory() : null;
	return [...filtered].sort((a, b) => {
		switch (sortBy) {
			case 'delay':
			case 'ping':
				return compareDelayLike(
					latestPositiveDelay(delayHistory!.get(a.tag) ?? []),
					latestPositiveDelay(delayHistory!.get(b.tag) ?? []),
					asc,
				);
			case 'server':
				return applyDirection(compareString(a.label || a.server || a.tag, b.label || b.server || b.tag), asc);
			case 'protocol':
				return applyDirection(compareString(singboxProtocolLabel(a.protocol), singboxProtocolLabel(b.protocol)), asc);
			case 'tag':
				return applyDirection(compareString(a.tag, b.tag), asc);
			case 'status':
				return applyDirection(compareBool(a.tag === activeMember, b.tag === activeMember), asc);
		}
	});
}
