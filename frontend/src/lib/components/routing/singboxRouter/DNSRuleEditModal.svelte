<script lang="ts">
	import {
		Button,
		Dropdown,
		ChipMultiSelect,
		SegmentedControl,
		type DropdownOption,
		type ChipOption,
		type SegmentedOption,
	} from '$lib/components/ui';
	import SingboxSettingsModal from './SingboxSettingsModal.svelte';
	import type { SingboxRouterDNSRule, SingboxRouterDNSServer, SingboxRouterRuleSet } from '$lib/types';
	import { dnsServerSubtitle } from '$lib/components/sb-router/dnsServerDetourDisplay';

	interface Props {
		rule?: SingboxRouterDNSRule;
		servers: SingboxRouterDNSServer[];
		availableRuleSets: SingboxRouterRuleSet[];
		/**
		 * Per-tag count of how many *other* DNS rules reference each rule_set.
		 * The currently edited rule must be excluded by the caller (use
		 * computeRuleSetUsage with excludeIndex=editIndex). Empty map is fine
		 * — all sets render as unused.
		 */
		ruleSetUsage?: Map<string, number>;
		onClose: () => void;
		onSave: (rule: SingboxRouterDNSRule) => Promise<void> | void;
	}
	let { rule, servers, availableRuleSets, ruleSetUsage, onClose, onSave }: Props = $props();

	function normalizeTags(tags: string[]): string[] {
		return [...new Set(tags.map((s) => s.trim()).filter(Boolean))];
	}

	const serverOptions = $derived<DropdownOption[]>([
		{ value: '', label: '— выберите —' },
		...servers.map((s) => ({
			value: s.tag,
			label: s.tag,
			description: dnsServerSubtitle(s),
		})),
	]);

	// svelte-ignore state_referenced_locally
	let ruleSetTags = $state<string[]>(rule?.rule_set ?? []);
	const ruleSetOptions = $derived<ChipOption[]>(
		availableRuleSets.map((rs) => ({
			value: rs.tag,
			label: rs.tag,
			usedCount: ruleSetUsage?.get(rs.tag) ?? 0,
		})),
	);
	// svelte-ignore state_referenced_locally
	let domainSuffixStr = $state((rule?.domain_suffix ?? []).join('\n'));
	// svelte-ignore state_referenced_locally
	let domainStr = $state((rule?.domain ?? []).join('\n'));
	// svelte-ignore state_referenced_locally
	let domainKeywordStr = $state((rule?.domain_keyword ?? []).join(', '));
	// svelte-ignore state_referenced_locally
	let domainRegexStr = $state((rule?.domain_regex ?? []).join('\n'));
	// svelte-ignore state_referenced_locally
	let queryTypeStr = $state((rule?.query_type ?? []).join(', '));

	function initAction(r?: SingboxRouterDNSRule): 'route' | 'block' {
		if (r?.action === 'reject' || r?.action === 'predefined') return 'block';
		return 'route';
	}
	function initBlockMethod(r?: SingboxRouterDNSRule): 'nxdomain' | 'refused' | 'drop' {
		if (r?.action === 'predefined') return 'nxdomain';
		if (r?.action === 'reject' && r?.method === 'drop') return 'drop';
		return 'refused';
	}
	// svelte-ignore state_referenced_locally
	let action = $state<'route' | 'block'>(initAction(rule));
	// svelte-ignore state_referenced_locally
	let blockMethod = $state<'nxdomain' | 'refused' | 'drop'>(initBlockMethod(rule));
	// svelte-ignore state_referenced_locally
	let server = $state(rule?.server ?? '');

	const blockMethodOptions = [
		{ value: 'nxdomain', label: 'NXDOMAIN (нет такого домена)' },
		{ value: 'refused', label: 'REFUSED' },
		{ value: 'drop', label: 'Drop (без ответа)' },
	];

	const actionOptions: SegmentedOption<'route' | 'block'>[] = [
		{ value: 'route', label: 'Резолвить' },
		{ value: 'block', label: 'Заблокировать' },
	];

	let busy = $state(false);
	let error = $state('');

	// Snapshot initial state for isDirty detection
	let initialRuleSetTagsSnapshot = $state<string[]>([]);
	let initialDomainSuffixStr = $state('');
	let initialDomainStr = $state('');
	let initialDomainKeywordStr = $state('');
	let initialDomainRegexStr = $state('');
	let initialQueryTypeStr = $state('');
	let initialAction: 'route' | 'block' = $state('route');
	let initialBlockMethod: 'nxdomain' | 'refused' | 'drop' = $state('refused');
	let initialServer = $state('');

	// Initialize snapshot when modal opens
	$effect(() => {
		if (rule) {
			initialRuleSetTagsSnapshot = [...(rule.rule_set ?? [])];
			initialDomainSuffixStr = (rule.domain_suffix ?? []).join('\n');
			initialDomainStr = (rule.domain ?? []).join('\n');
			initialDomainKeywordStr = (rule.domain_keyword ?? []).join(', ');
			initialDomainRegexStr = (rule.domain_regex ?? []).join('\n');
			initialQueryTypeStr = (rule.query_type ?? []).join(', ');
			initialAction = initAction(rule);
			initialBlockMethod = initBlockMethod(rule);
			initialServer = rule.server ?? '';
		} else {
			initialRuleSetTagsSnapshot = [];
			initialDomainSuffixStr = '';
			initialDomainStr = '';
			initialDomainKeywordStr = '';
			initialDomainRegexStr = '';
			initialQueryTypeStr = '';
			initialAction = 'route';
			initialBlockMethod = 'refused';
			initialServer = '';
		}
	});

	const isDirty = $derived.by(() => {
		return (
			normalizeTags(ruleSetTags).join(',') !== normalizeTags(initialRuleSetTagsSnapshot).join(',') ||
			domainSuffixStr !== initialDomainSuffixStr ||
			domainStr !== initialDomainStr ||
			domainKeywordStr !== initialDomainKeywordStr ||
			domainRegexStr !== initialDomainRegexStr ||
			queryTypeStr !== initialQueryTypeStr ||
			action !== initialAction ||
			blockMethod !== initialBlockMethod ||
			server !== initialServer
		);
	});

	async function save(): Promise<void> {
		busy = true;
		error = '';
		try {
			const rule_set = normalizeTags(ruleSetTags);
			const domain_suffix = domainSuffixStr.split('\n').map((s) => s.trim()).filter(Boolean);
			const domain = domainStr.split('\n').map((s) => s.trim()).filter(Boolean);
			const domain_keyword = domainKeywordStr.split(',').map((s) => s.trim()).filter(Boolean);
			const domain_regex = domainRegexStr.split('\n').map((s) => s.trim()).filter(Boolean);
			const query_type = queryTypeStr.split(',').map((s) => s.trim().toUpperCase()).filter(Boolean);

			const hasMatcher =
				rule_set.length > 0 ||
				domain_suffix.length > 0 ||
				domain.length > 0 ||
				domain_keyword.length > 0 ||
				domain_regex.length > 0 ||
				query_type.length > 0;
			if (!hasMatcher) {
				error = 'Нужен хотя бы один matcher';
				busy = false;
				return;
			}

			const built: SingboxRouterDNSRule = {
				rule_set: rule_set.length ? rule_set : undefined,
				domain_suffix: domain_suffix.length ? domain_suffix : undefined,
				domain: domain.length ? domain : undefined,
				domain_keyword: domain_keyword.length ? domain_keyword : undefined,
				domain_regex: domain_regex.length ? domain_regex : undefined,
				query_type: query_type.length ? query_type : undefined,
			};

			if (action === 'route') {
				if (!server) { error = 'Выберите DNS сервер'; busy = false; return; }
				built.action = 'route';
				built.server = server;
			} else if (blockMethod === 'nxdomain') {
				built.action = 'predefined';
				built.rcode = 'NXDOMAIN';
			} else if (blockMethod === 'drop') {
				built.action = 'reject';
				built.method = 'drop';
			} else {
				built.action = 'reject';
				built.method = 'default';
			}

			await onSave(built);
		} catch (e) {
			error = (e as Error).message;
		} finally {
			busy = false;
		}
	}
</script>

<SingboxSettingsModal
	title={rule ? 'Редактировать DNS правило' : 'Новое DNS правило'}
	onClose={onClose}
	size="lg"
	hasUnsavedChanges={() => isDirty}
>
	<div class="form">
		<div class="section-label">Matchers (минимум один)</div>

		<label class="field">
			<div class="lbl">Rule sets</div>
			<ChipMultiSelect
				values={ruleSetTags}
				options={ruleSetOptions}
				onchange={(next) => (ruleSetTags = next)}
				placeholder="не выбрано"
				allowOrphans
			/>
		</label>

		<label class="field">
			<div class="lbl">Domain suffix</div>
			<textarea bind:value={domainSuffixStr} rows="3" placeholder="по одному на строке: .youtube.com"></textarea>
		</label>

		<label class="field">
			<div class="lbl">Domain (точное совпадение)</div>
			<textarea bind:value={domainStr} rows="2" placeholder="example.com"></textarea>
		</label>

		<label class="field">
			<div class="lbl">Domain keyword (через запятую)</div>
			<input bind:value={domainKeywordStr} placeholder="tracker, analytics" />
		</label>

		<label class="field">
			<div class="lbl">Domain regex (по строке)</div>
			<textarea bind:value={domainRegexStr} rows="2" placeholder={"^ads?\\d+\\."}></textarea>
		</label>

		<label class="field">
			<div class="lbl">Query type (через запятую)</div>
			<input bind:value={queryTypeStr} placeholder="A, AAAA, HTTPS" />
		</label>

		<div class="action-section">
			<div class="section-label">Действие</div>
			<SegmentedControl
				value={action}
				options={actionOptions}
				ariaLabel="Действие DNS правила"
				onchange={(next) => (action = next)}
			/>

			{#if action === 'route'}
				<label class="field">
					<div class="lbl">DNS сервер</div>
					<Dropdown bind:value={server} options={serverOptions} fullWidth />
				</label>
			{:else}
				<label class="field">
					<div class="lbl">Метод блокировки</div>
					<Dropdown bind:value={blockMethod} options={blockMethodOptions} fullWidth />
				</label>
			{/if}
		</div>

		{#if error}<div class="error">{error}</div>{/if}
	</div>

	{#snippet actions()}
		<Button variant="ghost" size="md" onclick={onClose} type="button">Отмена</Button>
		<Button variant="primary" size="md" onclick={save} disabled={busy} loading={busy} type="button">
			Сохранить
		</Button>
	{/snippet}
</SingboxSettingsModal>
