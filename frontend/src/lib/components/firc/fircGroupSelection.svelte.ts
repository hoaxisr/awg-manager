/**
 * Массовое выделение: режим выбора групп, выбранные группы и выбранные
 * правила по каждой группе.
 */

export interface FircGroupSelectionController {
	readonly selectMode: boolean;
	readonly groupIds: Set<string>;
	/** Включить/выключить режим выбора; при выключении выделение сбрасывается. */
	toggleMode(): void;
	toggleGroup(id: string): void;
	setGroups(ids: Iterable<string>): void;
	rules(groupId: string): Set<string>;
	toggleRule(groupId: string, ruleId: string): void;
	setRules(groupId: string, ids: string[]): void;
}

export function createFircGroupSelection(): FircGroupSelectionController {
	let selectMode = $state(false);
	let groupIds = $state<Set<string>>(new Set());
	let rulesByGroup = $state<Record<string, Set<string>>>({});

	function rules(groupId: string): Set<string> {
		return rulesByGroup[groupId] ?? new Set<string>();
	}

	return {
		get selectMode() {
			return selectMode;
		},
		get groupIds() {
			return groupIds;
		},
		toggleMode() {
			selectMode = !selectMode;
			if (!selectMode) groupIds = new Set();
		},
		toggleGroup(id) {
			const next = new Set(groupIds);
			if (next.has(id)) next.delete(id);
			else next.add(id);
			groupIds = next;
		},
		setGroups(ids) {
			groupIds = new Set(ids);
		},
		rules,
		toggleRule(groupId, ruleId) {
			const next = new Set(rules(groupId));
			if (next.has(ruleId)) next.delete(ruleId);
			else next.add(ruleId);
			rulesByGroup = { ...rulesByGroup, [groupId]: next };
		},
		setRules(groupId, ids) {
			rulesByGroup = { ...rulesByGroup, [groupId]: new Set(ids) };
		},
	};
}
