import { m } from '$lib/i18n';
import type { FircDeviceSelector, FircGroup, FircListRule, FircRuleType } from '$lib/types/firc';
import { detectFircRuleType } from './fircBulkRules';
import { MANAGED_SERVER_SUBNETS } from './fircGroups';
import { isDnsOnlyGroup, isSubnetRuleType } from './fircResolve';

export { detectFircRuleType };

export type FircOverlapKind = 'shadowed' | 'split';

export interface FircRuleOverlap {
	kind: FircOverlapKind;
	groupId: string;
	groupName: string;
	/** Group-scoped overlap key (`fircOwnRuleOverlapId` or `fircListRuleOverlapId`). */
	ruleId: string;
	/** Original `rule.id` on the covering rule object. */
	rawRuleId: string;
	inSameGroup: boolean;
	isTargetListRule: boolean;
}

export interface FircListUrlOverlap {
	kind: FircOverlapKind;
	groupId: string;
	groupName: string;
	url: string;
}

export interface FircGroupDuplicateConflict {
	/** Group-scoped lookup key in ruleOverlaps (`fircOwnRuleOverlapId` or `fircListRuleOverlapId`). */
	ruleId: string;
	/** Original id on the rule object. */
	rawRuleId: string;
	isListRule: boolean;
	rulePattern: string;
	ruleType: string;
	groupId: string;
	sourceGroupId: string;
	sourceGroupName: string;
	/** Group-scoped lookup key of the covering rule. */
	sourceRuleId: string;
	/** Original id on the covering rule object. */
	sourceRawRuleId: string;
	sourceIsListRule: boolean;
	inSameGroup: boolean;
	duplicateKey: string;
	totalRulesWithSameKey: number;
	kind: FircOverlapKind;
}

export interface FircDuplicateAnalysis {
	ruleOverlaps: Map<string, FircRuleOverlap>;
	listUrlOverlaps: Map<string, FircListUrlOverlap>;
	duplicateRuleIds: Set<string>;
	duplicateGroupIds: Set<string>;
	duplicateConflictsByGroup: Map<string, FircGroupDuplicateConflict[]>;
	shadowedOwnRuleIdsByGroup: Map<string, Set<string>>;
	shadowedCountByGroup: Map<string, number>;
	splitCountByGroup: Map<string, number>;
}

export type FircImportEntryStatus =
	| 'new'
	| 'batch_duplicate'
	| 'same_group_duplicate'
	| 'other_group_shadowed'
	| 'other_group_split'
	| 'other_group_later'
	| 'unsupported_dns_only_subnet';

export interface FircImportEntry {
	lineNumber: number;
	raw: string;
	rule: string;
	type: FircRuleType;
	key: string;
	status: FircImportEntryStatus;
	/** True whenever an earlier enabled group shadows this key for the target group's devices (even if this entry is also a batch/same-group duplicate). */
	shadowedByUpper?: boolean;
	conflictGroupId?: string;
	conflictGroupName?: string;
}

export interface FircImportAnalysis {
	entries: FircImportEntry[];
	totalValid: number;
	newCount: number;
	batchDuplicateCount: number;
	sameGroupDuplicateCount: number;
	otherGroupShadowedCount: number;
	otherGroupSplitCount: number;
	otherGroupLaterCount: number;
	unsupportedDnsOnlySubnetCount: number;
	shadowedGroupNames: string[];
	otherNoticeGroupNames: string[];
}

export interface FircImportSelectionOptions {
	skipBatchAndSameGroup: boolean;
	skipOtherGroupShadowed: boolean;
}

export interface FircImportSelectionResult {
	selected: FircImportEntry[];
	skippedBatchOrSame: number;
	skippedOtherShadowed: number;
	skippedUnsupportedSubnet: number;
}

export function normalizeFircRuleType(type: string | undefined | null): string {
	const t = (type ?? 'namespace').trim().toLowerCase();
	if (!t) return 'namespace';
	if (t === 'wild' || t === 'dotwild') return 'wildcard';
	return t;
}

export function normalizeFircPorts(ports: string | undefined | null): string {
	if (!ports) return '';
	return ports
		.split(',')
		.map((part) => part.trim())
		.filter(Boolean)
		.join(',');
}

interface FircRuleLike {
	type: string;
	rule: string;
	enable?: boolean;
	proto?: string;
	ports?: string;
}

export function fircRuleKey(rule: FircRuleLike): string {
	const trimmed = (rule.rule ?? '').trim();
	if (!trimmed) return '';
	const normType = normalizeFircRuleType(rule.type);
	const normPattern = normType === 'regex' ? trimmed : trimmed.toLowerCase();
	const takesPorts = normType === 'subnet' || normType === 'subnet6';
	const normProto = takesPorts ? (rule.proto ?? '').trim().toLowerCase() : '';
	const normPorts = takesPorts && normProto ? normalizeFircPorts(rule.ports) : '';
	return `${normType}:${normPattern}:${normProto}:${normPorts}`;
}

export function fircOwnRuleOverlapId(groupId: string, ruleId: string): string {
	return `own:${groupId}:${ruleId}`;
}

export function fircListRuleOverlapId(groupId: string, listRuleId: string): string {
	return `list:${groupId}:${listRuleId}`;
}

export function normalizeFircListUrl(url: string | undefined | null): string {
	const trimmed = (url ?? '').trim();
	if (!trimmed) return '';
	try {
		const parsed = new URL(trimmed);
		parsed.protocol = parsed.protocol.toLowerCase();
		parsed.hostname = parsed.hostname.toLowerCase();
		return parsed.toString();
	} catch {
		return trimmed;
	}
}

function deviceEntries(list: readonly string[] | undefined, stripManagedSubnets = false): Set<string> {
	const out = new Set<string>();
	if (!list) return out;
	for (const raw of list) {
		const trimmed = raw.trim();
		if (!trimmed) continue;
		if (stripManagedSubnets && MANAGED_SERVER_SUBNETS.has(trimmed)) continue;
		out.add(trimmed.toLowerCase());
	}
	return out;
}

/**
 * Returns true if `upper`'s device selector admits every device admitted by `lower`.
 *
 * Managed inbound VPN server subnets injected automatically by AWGM into `allow`
 * are ignored so unsaved edits match persisted groups accurately.
 */
export function admitsEveryDeviceOf(
	upper: FircDeviceSelector | undefined,
	lower: FircDeviceSelector | undefined,
): boolean {
	const upperAllow = deviceEntries(upper?.allow, true);
	const upperDeny = deviceEntries(upper?.deny, false);
	if (upperAllow.size === 0 && upperDeny.size === 0) return true;

	const lowerAllow = deviceEntries(lower?.allow, true);
	const lowerDeny = deviceEntries(lower?.deny, false);

	if (upperAllow.size > 0) {
		if (lowerAllow.size === 0) return false;
		for (const item of lowerAllow) {
			if (!upperAllow.has(item)) return false;
		}
	}
	for (const item of upperDeny) {
		if (!lowerDeny.has(item)) return false;
	}
	return true;
}

interface EarlierRuleRef {
	group: FircGroup;
	entryId: string;
	rawRuleId: string;
	isListRule: boolean;
}

interface EarlierListRef {
	group: FircGroup;
}

function displayGroupName(name: string | undefined): string {
	const trimmed = (name ?? '').trim();
	return trimmed || m.firc_dup_unnamed_group();
}

export function computeFircDuplicates(
	groups: readonly FircGroup[],
	listRulesByGroup?: Readonly<Record<string, readonly FircListRule[]>>,
): FircDuplicateAnalysis {
	const earlier = new Map<string, EarlierRuleRef[]>();
	const earlierListUrls = new Map<string, EarlierListRef[]>();
	const ruleOverlaps = new Map<string, FircRuleOverlap>();
	const listUrlOverlaps = new Map<string, FircListUrlOverlap>();
	const copies = new Map<string, number>();

	for (const group of groups) {
		const groupEnabled = group.enable !== false;

		const normListUrl = normalizeFircListUrl(group.list?.url);
		if (normListUrl) {
			let beforeUrl = earlierListUrls.get(normListUrl);
			if (!beforeUrl) {
				beforeUrl = [];
				earlierListUrls.set(normListUrl, beforeUrl);
			}
			if (beforeUrl.length > 0) {
				const shadow = beforeUrl.find((e) => admitsEveryDeviceOf(e.group.devices, group.devices));
				const named = shadow ?? beforeUrl[0];
				listUrlOverlaps.set(group.id, {
					kind: shadow ? 'shadowed' : 'split',
					groupId: named.group.id,
					groupName: displayGroupName(named.group.name),
					url: group.list?.url?.trim() ?? '',
				});
			}
			if (groupEnabled) {
				beforeUrl.push({ group });
			}
		}

		const sameGroupEarlier = new Map<string, EarlierRuleRef>();

		const inspectRule = (
			entryId: string,
			rawRuleId: string,
			rule: FircRuleLike,
			isListRule: boolean,
		) => {
			const key = fircRuleKey(rule);
			if (!key) return;
			copies.set(key, (copies.get(key) ?? 0) + 1);

			let before = earlier.get(key);
			if (!before) {
				before = [];
				earlier.set(key, before);
			}
			const shadow =
				before.find((e) => admitsEveryDeviceOf(e.group.devices, group.devices)) ??
				sameGroupEarlier.get(key);
			if (shadow) {
				ruleOverlaps.set(entryId, {
					kind: 'shadowed',
					groupId: shadow.group.id,
					groupName: displayGroupName(shadow.group.name),
					ruleId: shadow.entryId,
					rawRuleId: shadow.rawRuleId,
					inSameGroup: shadow.group.id === group.id,
					isTargetListRule: shadow.isListRule,
				});
			} else if (before.length > 0) {
				const named = before[0];
				ruleOverlaps.set(entryId, {
					kind: 'split',
					groupId: named.group.id,
					groupName: displayGroupName(named.group.name),
					ruleId: named.entryId,
					rawRuleId: named.rawRuleId,
					inSameGroup: false,
					isTargetListRule: named.isListRule,
				});
			}
			if (rule.enable !== false) {
				if (!sameGroupEarlier.has(key)) {
					sameGroupEarlier.set(key, { group, entryId, rawRuleId, isListRule });
				}
				if (groupEnabled) {
					before.push({ group, entryId, rawRuleId, isListRule });
				}
			}
		};

		// Inspect loaded remote list rules before manual rules within the same group so
		// redundant manual copies are flagged as shadowed and removable via 1-click cleanup.
		const loadedListRules = listRulesByGroup?.[group.id];
		if (loadedListRules) {
			for (const lrule of loadedListRules) {
				inspectRule(fircListRuleOverlapId(group.id, lrule.id), lrule.id, lrule, true);
			}
		}
		for (const rule of group.rules ?? []) {
			inspectRule(fircOwnRuleOverlapId(group.id, rule.id), rule.id, rule, false);
		}
	}

	const duplicateRuleIds = new Set<string>();
	const duplicateGroupIds = new Set<string>();
	const duplicateConflictsByGroup = new Map<string, FircGroupDuplicateConflict[]>();
	const shadowedOwnRuleIdsByGroup = new Map<string, Set<string>>();
	const shadowedCountByGroup = new Map<string, number>();
	const splitCountByGroup = new Map<string, number>();

	for (const group of groups) {
		const conflicts: FircGroupDuplicateConflict[] = [];
		const shadowedOwn = new Set<string>();
		let shadowedCount = 0;
		let splitCount = 0;

		const collect = (
			entryId: string,
			rawRuleId: string,
			rule: FircRuleLike,
			isListRule: boolean,
		) => {
			const overlap = ruleOverlaps.get(entryId);
			if (!overlap) return;
			const key = fircRuleKey(rule);
			duplicateRuleIds.add(entryId);
			duplicateRuleIds.add(overlap.ruleId);
			if (overlap.kind === 'shadowed') {
				shadowedCount++;
				if (!isListRule) shadowedOwn.add(rawRuleId);
			} else {
				splitCount++;
			}
			conflicts.push({
				ruleId: entryId,
				rawRuleId,
				isListRule,
				rulePattern: (rule.rule ?? '').trim(),
				ruleType: normalizeFircRuleType(rule.type),
				groupId: group.id,
				sourceGroupId: overlap.groupId,
				sourceGroupName: overlap.groupName,
				sourceRuleId: overlap.ruleId,
				sourceRawRuleId: overlap.rawRuleId,
				sourceIsListRule: overlap.isTargetListRule,
				inSameGroup: overlap.inSameGroup,
				duplicateKey: key,
				totalRulesWithSameKey: copies.get(key) ?? 2,
				kind: overlap.kind,
			});
		};

		for (const rule of group.rules ?? []) {
			collect(fircOwnRuleOverlapId(group.id, rule.id), rule.id, rule, false);
		}
		const loadedListRules = listRulesByGroup?.[group.id];
		if (loadedListRules) {
			for (const lrule of loadedListRules) {
				collect(fircListRuleOverlapId(group.id, lrule.id), lrule.id, lrule, true);
			}
		}

		if (conflicts.length > 0) {
			conflicts.sort((a, b) => {
				if (a.kind !== b.kind) return a.kind === 'shadowed' ? -1 : 1;
				if (a.inSameGroup !== b.inSameGroup) return a.inSameGroup ? -1 : 1;
				return a.rulePattern.localeCompare(b.rulePattern);
			});
			duplicateGroupIds.add(group.id);
			duplicateConflictsByGroup.set(group.id, conflicts);
			if (shadowedOwn.size > 0) shadowedOwnRuleIdsByGroup.set(group.id, shadowedOwn);
			if (shadowedCount > 0) shadowedCountByGroup.set(group.id, shadowedCount);
			if (splitCount > 0) splitCountByGroup.set(group.id, splitCount);
		} else if (listUrlOverlaps.has(group.id)) {
			duplicateGroupIds.add(group.id);
		}
	}

	return {
		ruleOverlaps,
		listUrlOverlaps,
		duplicateRuleIds,
		duplicateGroupIds,
		duplicateConflictsByGroup,
		shadowedOwnRuleIdsByGroup,
		shadowedCountByGroup,
		splitCountByGroup,
	};
}

export function formatFircOverlapTooltip(overlap: FircRuleOverlap): string {
	if (overlap.inSameGroup) {
		return overlap.isTargetListRule
			? m.firc_dup_tip_same_group_list()
			: m.firc_dup_tip_same_group_own();
	}
	if (overlap.kind === 'shadowed') {
		return m.firc_dup_tip_shadowed({ group: overlap.groupName });
	}
	return m.firc_dup_tip_split({ group: overlap.groupName });
}

export function analyzeFircImportText(
	text: string,
	targetGroupIndex: number,
	groups: readonly FircGroup[],
	listRulesByGroup?: Readonly<Record<string, readonly FircListRule[]>>,
	forcedType: 'auto' | FircRuleType = 'auto',
): FircImportAnalysis {
	const targetGroup =
		targetGroupIndex >= 0 && targetGroupIndex < groups.length
			? groups[targetGroupIndex]
			: undefined;
	const targetDnsOnly = Boolean(targetGroup && isDnsOnlyGroup(targetGroup));
	const sameGroupKeys = new Set<string>();
	const earlierByKey = new Map<string, FircGroup[]>();
	const laterByKey = new Map<string, FircGroup[]>();

	if (targetGroup) {
		for (const r of targetGroup.rules ?? []) {
			const k = fircRuleKey(r);
			if (k) sameGroupKeys.add(k);
		}
		for (const lr of listRulesByGroup?.[targetGroup.id] ?? []) {
			if (lr.enable === false) continue;
			const k = fircRuleKey(lr);
			if (k) sameGroupKeys.add(k);
		}

		for (let i = 0; i < groups.length; i++) {
			if (i === targetGroupIndex) continue;
			const g = groups[i];
			if (g.enable === false) continue;
			const map = i < targetGroupIndex ? earlierByKey : laterByKey;
			const record = (r: FircRuleLike) => {
				if (r.enable === false) return;
				const k = fircRuleKey(r);
				if (!k) return;
				let arr = map.get(k);
				if (!arr) {
					arr = [];
					map.set(k, arr);
				}
				if (!arr.includes(g)) arr.push(g);
			};
			for (const r of g.rules ?? []) record(r);
			for (const lr of listRulesByGroup?.[g.id] ?? []) record(lr);
		}
	}

	const seenInBatch = new Set<string>();
	const entries: FircImportEntry[] = [];
	const shadowedGroupSet = new Set<string>();
	const otherNoticeGroupSet = new Set<string>();

	let newCount = 0;
	let batchDuplicateCount = 0;
	let sameGroupDuplicateCount = 0;
	let otherGroupShadowedCount = 0;
	let otherGroupSplitCount = 0;
	let otherGroupLaterCount = 0;
	let unsupportedDnsOnlySubnetCount = 0;

	const rawLines = text.split(/\r?\n/);
	for (let idx = 0; idx < rawLines.length; idx++) {
		const raw = rawLines[idx];
		const trimmed = raw.trim();
		if (!trimmed || trimmed.startsWith('#')) continue;

		const type: FircRuleType = forcedType === 'auto' ? detectFircRuleType(trimmed) : forcedType;
		const key = fircRuleKey({ type, rule: trimmed });
		if (!key) continue;

		if (targetDnsOnly && isSubnetRuleType(type)) {
			unsupportedDnsOnlySubnetCount++;
			entries.push({
				lineNumber: idx + 1,
				raw,
				rule: trimmed,
				type,
				key,
				status: 'unsupported_dns_only_subnet',
			});
			continue;
		}

		const earlier = earlierByKey.get(key);
		const shadowGroup =
			earlier && earlier.length > 0 && targetGroup
				? earlier.find((eg) => admitsEveryDeviceOf(eg.devices, targetGroup.devices))
				: undefined;
		const shadowedByUpper = Boolean(shadowGroup);
		if (shadowGroup) {
			shadowedGroupSet.add(displayGroupName(shadowGroup.name));
		}

		let status: FircImportEntryStatus = 'new';
		let conflictGroupId: string | undefined;
		let conflictGroupName: string | undefined;

		if (seenInBatch.has(key)) {
			status = 'batch_duplicate';
			batchDuplicateCount++;
			if (shadowGroup) {
				conflictGroupId = shadowGroup.id;
				conflictGroupName = displayGroupName(shadowGroup.name);
			}
		} else {
			seenInBatch.add(key);
			if (sameGroupKeys.has(key)) {
				status = 'same_group_duplicate';
				sameGroupDuplicateCount++;
				if (shadowGroup) {
					conflictGroupId = shadowGroup.id;
					conflictGroupName = displayGroupName(shadowGroup.name);
				}
			} else if (shadowGroup) {
				status = 'other_group_shadowed';
				conflictGroupId = shadowGroup.id;
				conflictGroupName = displayGroupName(shadowGroup.name);
				otherGroupShadowedCount++;
			} else if (earlier && earlier.length > 0) {
				status = 'other_group_split';
				conflictGroupId = earlier[0].id;
				conflictGroupName = displayGroupName(earlier[0].name);
				otherNoticeGroupSet.add(conflictGroupName);
				otherGroupSplitCount++;
			} else {
				const later = laterByKey.get(key);
				if (later && later.length > 0) {
					status = 'other_group_later';
					conflictGroupId = later[0].id;
					conflictGroupName = displayGroupName(later[0].name);
					otherNoticeGroupSet.add(conflictGroupName);
					otherGroupLaterCount++;
				} else {
					status = 'new';
					newCount++;
				}
			}
		}

		entries.push({
			lineNumber: idx + 1,
			raw,
			rule: trimmed,
			type,
			key,
			status,
			...(shadowedByUpper ? { shadowedByUpper: true } : {}),
			conflictGroupId,
			conflictGroupName,
		});
	}

	return {
		entries,
		totalValid: entries.length,
		newCount,
		batchDuplicateCount,
		sameGroupDuplicateCount,
		otherGroupShadowedCount,
		otherGroupSplitCount,
		otherGroupLaterCount,
		unsupportedDnsOnlySubnetCount,
		shadowedGroupNames: Array.from(shadowedGroupSet),
		otherNoticeGroupNames: Array.from(otherNoticeGroupSet),
	};
}

export function selectFircImportEntries(
	analysis: FircImportAnalysis,
	options: FircImportSelectionOptions,
): FircImportSelectionResult {
	const selected: FircImportEntry[] = [];
	let skippedBatchOrSame = 0;
	let skippedOtherShadowed = 0;
	let skippedUnsupportedSubnet = 0;

	for (const entry of analysis.entries) {
		if (entry.status === 'unsupported_dns_only_subnet') {
			skippedUnsupportedSubnet++;
			continue;
		}
		if (
			options.skipBatchAndSameGroup &&
			(entry.status === 'batch_duplicate' || entry.status === 'same_group_duplicate')
		) {
			skippedBatchOrSame++;
			continue;
		}
		if (
			options.skipOtherGroupShadowed &&
			(entry.status === 'other_group_shadowed' || entry.shadowedByUpper)
		) {
			skippedOtherShadowed++;
			continue;
		}
		selected.push(entry);
	}

	return { selected, skippedBatchOrSame, skippedOtherShadowed, skippedUnsupportedSubnet };
}
