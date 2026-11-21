import type { FircDeviceSelector, FircGroup, FircRule, FircRuleType } from '$lib/types/firc';

const TYPE_PREFIX_MAP: Record<string, FircRuleType> = {
	namespace: 'namespace',
	domain: 'domain',
	wildcard: 'wildcard',
	regex: 'regex',
	subnet: 'subnet',
	subnet6: 'subnet6',
};

const PROTO_MAP: Record<string, 'tcp' | 'udp'> = {
	tcp: 'tcp',
	udp: 'udp',
};

export interface ParsedRulesResult {
	rules: FircRule[];
	added: number;
	removed: number;
	kept: number;
	disabled: number;
}

export type BulkRuleAction =
	| { kind: 'enable'; enable: boolean }
	| { kind: 'setType'; type: FircRuleType }
	| { kind: 'move'; targetGroupId: string }
	| { kind: 'delete' };

export type BulkGroupAction =
	| { kind: 'enable'; enable: boolean }
	| { kind: 'setInterface'; iface: string | string[] }
	| { kind: 'setDevices'; devices: FircDeviceSelector }
	| { kind: 'delete' };

/** Auto-detect FIRC rule type from raw pattern syntax. */
export function detectFircRuleType(line: string): FircRuleType {
	const t = line.trim();
	if (/^\d{1,3}(\.\d{1,3}){3}(\/\d{1,2})?$/.test(t)) return 'subnet';
	if (t.includes(':') && /^[0-9a-fA-F:/]+$/.test(t)) return 'subnet6';
	if (t.includes('*')) return 'wildcard';
	if (t.startsWith('^') || t.endsWith('$') || t.includes('\\')) return 'regex';
	return 'namespace';
}

/** Serialize custom FIRC rules into a lossless multiline text representation. */
export function serializeRulesToText(rules: FircRule[]): string {
	const lines: string[] = [];
	for (const r of rules) {
		const pattern = (r.rule ?? '').trim();
		if (!pattern) continue;
		const prefixOff = r.enable ? '' : '[off] ';
		const autoType = detectFircRuleType(pattern);
		const typePrefix = r.type && r.type !== autoType ? `${r.type}:` : '';
		const protoPart =
			(r.type === 'subnet' || r.type === 'subnet6') && r.proto
				? ` ${r.proto}${r.ports?.trim() ? `:${r.ports.trim()}` : ''}`
				: '';
		const commentPart = r.comment?.trim() ? ` # ${r.comment.trim()}` : '';
		lines.push(`${prefixOff}${typePrefix}${pattern}${protoPart}${commentPart}`);
	}
	return lines.join('\n');
}

/**
 * Parse multiline text into FIRC rules while preserving IDs and autolist
 * metadata for rules whose (type, rule) pair still matches existing entries.
 */
export function parseRulesFromText(
	text: string,
	existingRules: FircRule[],
	makeId: () => string,
): ParsedRulesResult {
	const pool = new Map<string, FircRule[]>();
	for (const r of existingRules) {
		const key = `${r.type}\0${r.rule}`;
		const list = pool.get(key);
		if (list) list.push(r);
		else pool.set(key, [r]);
	}

	const rules: FircRule[] = [];
	let kept = 0;
	let added = 0;
	let disabled = 0;

	for (const raw of text.split(/\r?\n/)) {
		let line = raw.trim();
		if (!line || line.startsWith('#')) continue;

		let enable = true;
		const offMatch = line.match(/^\[off\]\s+(.*)$/i);
		if (offMatch) {
			enable = false;
			line = offMatch[1].trim();
			if (!line || line.startsWith('#')) continue;
		}

		let comment = '';
		const commentMatch = line.match(/^(.*?)\s+#\s*(.*)$/);
		if (commentMatch) {
			line = commentMatch[1].trim();
			comment = commentMatch[2].trim();
		}
		if (!line) continue;

		let explicitType: FircRuleType | null = null;
		const typePrefixMatch = line.match(/^(namespace|domain|wildcard|regex|subnet6|subnet):(.*)$/i);
		if (typePrefixMatch) {
			explicitType = TYPE_PREFIX_MAP[typePrefixMatch[1].toLowerCase()] ?? null;
			line = typePrefixMatch[2].trim();
		}
		if (!line) continue;

		let proto: 'tcp' | 'udp' | undefined;
		let ports: string | undefined;
		const protoMatch = line.match(/^(\S+)\s+(tcp|udp)(?::([^\s]+))?$/i);
		if (protoMatch) {
			const candidatePattern = protoMatch[1].trim();
			const candidateDetected = detectFircRuleType(candidatePattern);
			if (
				explicitType === 'subnet' ||
				explicitType === 'subnet6' ||
				(explicitType === null &&
					(candidateDetected === 'subnet' || candidateDetected === 'subnet6'))
			) {
				line = candidatePattern;
				proto = PROTO_MAP[protoMatch[2].toLowerCase()];
				ports = protoMatch[3]?.trim() || undefined;
			}
		}

		const type: FircRuleType = explicitType ?? detectFircRuleType(line);
		const poolKey = `${type}\0${line}`;
		const matched = pool.get(poolKey)?.shift();
		if (matched) kept += 1;
		else added += 1;
		if (!enable) disabled += 1;

		const nextRule: FircRule = {
			id: matched?.id ?? makeId(),
			type,
			rule: line,
			enable,
		};
		if (type === 'subnet' || type === 'subnet6') {
			if (proto) nextRule.proto = proto;
			if (proto && ports) nextRule.ports = ports;
		}
		if (comment) nextRule.comment = comment;
		if (matched?.autolist) nextRule.autolist = matched.autolist;
		if (matched?.autolistByDomain) nextRule.autolistByDomain = matched.autolistByDomain;
		if (matched?.name) nextRule.name = matched.name;
		rules.push(nextRule);
	}

	const removed = Math.max(0, existingRules.length - kept);
	return { rules, added, removed, kept, disabled };
}

/** Apply a bulk action to selected custom rules within a single FIRC group. */
export function applyBulkRuleAction(
	groups: FircGroup[],
	groupIndex: number,
	selectedIds: Set<string>,
	action: BulkRuleAction,
): { groups: FircGroup[]; affected: number } {
	const src = groups[groupIndex];
	if (!src || selectedIds.size === 0) {
		return { groups, affected: 0 };
	}

	if (action.kind === 'move') {
		if (!action.targetGroupId || action.targetGroupId === src.id) {
			return { groups, affected: 0 };
		}
		const targetIndex = groups.findIndex((g) => g.id === action.targetGroupId);
		if (targetIndex < 0) {
			return { groups, affected: 0 };
		}
		const moved = src.rules.filter((r) => selectedIds.has(r.id));
		if (moved.length === 0) {
			return { groups, affected: 0 };
		}
		const remaining = src.rules.filter((r) => !selectedIds.has(r.id));
		const next = groups.map((g, idx) => {
			if (idx === groupIndex) return { ...g, rules: remaining };
			if (idx === targetIndex) return { ...g, rules: [...moved, ...g.rules] };
			return g;
		});
		return { groups: next, affected: moved.length };
	}

	if (action.kind === 'delete') {
		const remaining = src.rules.filter((r) => !selectedIds.has(r.id));
		const affected = src.rules.length - remaining.length;
		if (affected === 0) return { groups, affected: 0 };
		const next = groups.map((g, idx) => (idx === groupIndex ? { ...g, rules: remaining } : g));
		return { groups: next, affected };
	}

	let affected = 0;
	const updatedRules = src.rules.map((r) => {
		if (!selectedIds.has(r.id)) return r;
		affected += 1;
		if (action.kind === 'enable') {
			return { ...r, enable: action.enable };
		}
		const isSubnet = action.type === 'subnet' || action.type === 'subnet6';
		if (isSubnet) {
			return { ...r, type: action.type };
		}
		const { proto: _p, ports: _pts, ...rest } = r;
		return { ...rest, type: action.type };
	});
	if (affected === 0) return { groups, affected: 0 };
	const next = groups.map((g, idx) => (idx === groupIndex ? { ...g, rules: updatedRules } : g));
	return { groups: next, affected };
}

/** Apply a bulk action to all selected FIRC groups. */
export function applyBulkGroupAction(
	groups: FircGroup[],
	selectedIds: Set<string>,
	action: BulkGroupAction,
): { groups: FircGroup[]; affected: number } {
	if (selectedIds.size === 0) {
		return { groups, affected: 0 };
	}

	if (action.kind === 'delete') {
		const remaining = groups.filter((g) => !selectedIds.has(g.id));
		const affected = groups.length - remaining.length;
		if (affected === 0) return { groups, affected: 0 };
		return { groups: remaining, affected };
	}

	let affected = 0;
	const next = groups.map((g) => {
		if (!selectedIds.has(g.id)) return g;
		affected += 1;
		if (action.kind === 'enable') {
			return { ...g, enable: action.enable };
		}
		if (action.kind === 'setInterface') {
			const nextIface = Array.isArray(action.iface) ? [...action.iface] : action.iface;
			const needsDefaultCheck =
				Array.isArray(action.iface) && action.iface.length > 1 && !g.check;
			return needsDefaultCheck
				? {
						...g,
						interface: nextIface,
						check: { mode: 'none' as const, url: '' },
					}
				: { ...g, interface: nextIface };
		}
		return {
			...g,
			devices: {
				allow: [...action.devices.allow],
				deny: [...action.devices.deny],
			},
		};
	});
	if (affected === 0) return { groups, affected: 0 };
	return { groups: next, affected };
}
