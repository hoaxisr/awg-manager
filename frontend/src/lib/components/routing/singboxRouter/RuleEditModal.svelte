<script lang="ts">
	import { onMount } from 'svelte';
	import SingboxSettingsModal from './SingboxSettingsModal.svelte';
	import {
		Button,
		Dropdown,
		ChipMultiSelect,
		SegmentedControl,
		type DropdownOption,
		type ChipOption,
		type SegmentedOption,
	} from '$lib/components/ui';
	import { api } from '$lib/api/client';
	import type { PolicyDevice, SingboxRouterRule, SingboxRouterRuleSet } from '$lib/types';
	import { flattenRouterRule } from '$lib/utils/routerRuleShape';
	import type { OutboundGroup } from './outboundOptions';

	interface Props {
		rule?: SingboxRouterRule;
		outboundOptions: OutboundGroup[];
		availableRuleSets: SingboxRouterRuleSet[];
		/**
		 * Pre-populated rule_set tags for the add-rule path (when `rule` is
		 * undefined). Used by the deep-link prefill flow from rule set cards.
		 * Ignored when `rule` is provided (edit mode reads rule.rule_set).
		 */
		initialRuleSetTags?: string[];
		/**
		 * Per-tag count of how many *other* router rules reference each
		 * rule_set. The currently edited rule must be excluded by the caller
		 * (use computeRuleSetUsage with excludeIndex=editIndex). Empty map
		 * is fine — all sets render as unused.
		 */
		ruleSetUsage?: Map<string, number>;
		/** Только domain_suffix и ip_cidr; outbound/action не меняются. */
		matchersOnly?: boolean;
		onClose: () => void;
		onSave: (rule: SingboxRouterRule) => Promise<void> | void;
	}
	let {
		rule,
		outboundOptions,
		availableRuleSets,
		initialRuleSetTags,
		ruleSetUsage,
		matchersOnly = false,
		onClose,
		onSave,
	}: Props = $props();

	// Правило «пресет ИЛИ свои адреса» хранится логической формой — редактор
	// работает с её плоским видом, иначе поля откроются пустыми и сохранение
	// сотрёт содержимое веток.
	const flat = (r: SingboxRouterRule | undefined): SingboxRouterRule | undefined =>
		r ? flattenRouterRule(r) : undefined;

	const outboundDropdownOptions = $derived<DropdownOption[]>([
		{ value: '', label: '— выберите —' },
		...outboundOptions.flatMap((g) =>
			g.items.map((i) => ({ value: i.value, label: i.label, group: g.group })),
		),
	]);

	// svelte-ignore state_referenced_locally
	let domainSuffixStr = $state((flat(rule)?.domain_suffix ?? []).join('\n'));
	// svelte-ignore state_referenced_locally
	let ipCidrStr = $state((flat(rule)?.ip_cidr ?? []).join('\n'));
	// svelte-ignore state_referenced_locally
	let sourceIpCidrStr = $state((flat(rule)?.source_ip_cidr ?? []).join('\n'));
	// svelte-ignore state_referenced_locally
	let sourceMacStr = $state((flat(rule)?.source_mac_address ?? []).join('\n'));

	// Пикер устройств LAN для поля «MAC устройства»: список грузится один раз
	// при открытии модала (без опроса); родители модала не держат общий стор
	// устройств, поэтому запрос делает сам модал. Ошибку загрузки не показываем —
	// пикер просто не появляется, textarea продолжает работать вручную.
	let pickerDevices = $state<PolicyDevice[]>([]);
	let pickerValue = $state('');
	onMount(() => {
		api.listPolicyDevices()
			.then((list) => (pickerDevices = list))
			.catch(() => {});
	});
	const pickerAddedMacs = $derived(new Set(parseLines(sourceMacStr).map((s) => s.toLowerCase())));
	const pickerOptions = $derived<DropdownOption[]>(
		[...pickerDevices]
			.sort((a, b) => Number(b.active) - Number(a.active))
			.map((d) => {
				const label = `${d.name || d.hostname || d.ip} · ${d.mac}`;
				return {
					value: d.mac,
					label: pickerAddedMacs.has(d.mac.toLowerCase()) ? `${label} (уже добавлен)` : label,
				};
			}),
	);
	function pickDevice(mac: string): void {
		const normalized = mac.toLowerCase();
		if (!pickerAddedMacs.has(normalized)) {
			sourceMacStr = sourceMacStr ? `${sourceMacStr}\n${normalized}` : normalized;
		}
		pickerValue = '';
	}
	// svelte-ignore state_referenced_locally
	let ruleSetTags = $state<string[]>(flat(rule)?.rule_set ?? initialRuleSetTags ?? []);
	const ruleSetOptions = $derived<ChipOption[]>(
		availableRuleSets.map((rs) => ({
			value: rs.tag,
			label: rs.tag,
			usedCount: ruleSetUsage?.get(rs.tag) ?? 0,
		})),
	);
	// svelte-ignore state_referenced_locally
	let portStr = $state((flat(rule)?.port ?? []).join(', '));
	// L4 matcher: empty = any (omit from JSON). Expert-only; simple mode treats
	// network as a complex field and won't open this editor for such rules.
	type NetworkFilter = '' | 'tcp' | 'udp';
	// svelte-ignore state_referenced_locally
	let network = $state<NetworkFilter>(
		flat(rule)?.network === 'tcp' || flat(rule)?.network === 'udp'
			? (flat(rule)!.network as NetworkFilter)
			: '',
	);

	// svelte-ignore state_referenced_locally
	let action: 'route' | 'reject' = $state((rule?.action === 'reject' ? 'reject' : 'route'));
	// svelte-ignore state_referenced_locally
	let outbound = $state(rule?.outbound ?? '');

	const actionOptions: SegmentedOption<'route' | 'reject'>[] = [
		{ value: 'route', label: 'Направить' },
		{ value: 'reject', label: 'Заблокировать' },
	];

	const networkOptions: SegmentedOption<NetworkFilter>[] = [
		{ value: '', label: 'Любой' },
		{ value: 'tcp', label: 'TCP' },
		{ value: 'udp', label: 'UDP' },
	];

	let busy = $state(false);
	let error = $state('');

	// Snapshot initial state for isDirty detection
	let initialDomainSuffixStr = $state('');
	let initialIpCidrStr = $state('');
	let initialSourceIpCidrStr = $state('');
	let initialSourceMacStr = $state('');
	let initialRuleSetTagsSnapshot = $state<string[]>([]);
	let initialPortStr = $state('');
	let initialNetwork: NetworkFilter = $state('');
	let initialAction: 'route' | 'reject' = $state('route');
	let initialOutbound = $state('');

	// Initialize snapshot when modal opens
	$effect(() => {
		const src = flat(rule);
		if (src) {
			initialDomainSuffixStr = (src.domain_suffix ?? []).join('\n');
			initialIpCidrStr = (src.ip_cidr ?? []).join('\n');
			initialSourceIpCidrStr = (src.source_ip_cidr ?? []).join('\n');
			initialSourceMacStr = (src.source_mac_address ?? []).join('\n');
			initialRuleSetTagsSnapshot = [...(src.rule_set ?? [])];
			initialPortStr = (src.port ?? []).join(', ');
			initialNetwork = src.network === 'tcp' || src.network === 'udp' ? src.network : '';
			initialAction = src.action === 'reject' ? 'reject' : 'route';
			initialOutbound = src.outbound ?? '';
		} else {
			initialDomainSuffixStr = '';
			initialIpCidrStr = '';
			initialSourceIpCidrStr = '';
			initialSourceMacStr = '';
			initialRuleSetTagsSnapshot = [...(initialRuleSetTags ?? [])];
			initialPortStr = '';
			initialNetwork = '';
			initialAction = 'route';
			initialOutbound = '';
		}
	});

	const isDirty = $derived.by(() => {
		return (
			domainSuffixStr !== initialDomainSuffixStr ||
			ipCidrStr !== initialIpCidrStr ||
			sourceIpCidrStr !== initialSourceIpCidrStr ||
			sourceMacStr !== initialSourceMacStr ||
			[...ruleSetTags].join(',') !== [...initialRuleSetTagsSnapshot].join(',') ||
			portStr !== initialPortStr ||
			network !== initialNetwork ||
			action !== initialAction ||
			outbound !== initialOutbound
		);
	});

	function parseLines(text: string): string[] {
		return text.split('\n').map((s) => s.trim()).filter(Boolean);
	}

	// Условия правила, для которых в форме нет поля. Они переносятся при
	// сохранении как есть (см. carried в save), поэтому форма обязана о них
	// сказать: иначе она выглядит полнее правила, чем оно есть.
	// Логическое правило, которое flattenRouterRule не узнал (чужая форма из
	// импорта или ручной правки): форма его не показывает и при сохранении
	// заменит собой. Молчать об этом нельзя — уничтожение чужой структуры
	// должно быть осознанным решением, а не побочным эффектом «Сохранить».
	const unflattenedLogical = $derived(flat(rule)?.type === 'logical');

	const hiddenMatchers = $derived.by(() => {
		const src = flat(rule);
		if (!src) return [];
		const out: string[] = [];
		if (src.domain?.length) out.push(`точные домены: ${src.domain.join(', ')}`);
		if (src.protocol) out.push(`прикладной протокол: ${src.protocol}`);
		if (src.ip_is_private) out.push('только локальные адреса назначения');
		if (src.inbound?.length) out.push(`вход: ${src.inbound.join(', ')}`);
		return out;
	});

	const domainsCount = $derived(parseLines(domainSuffixStr).length);
	const ipsCount = $derived(parseLines(ipCidrStr).length);
	const sourceIPsCount = $derived(parseLines(sourceIpCidrStr).length);
	const sourceMacCount = $derived(parseLines(sourceMacStr).length);

	async function save(): Promise<void> {
		busy = true;
		error = '';
		try {
			const domain_suffix = parseLines(domainSuffixStr);
			const ip_cidr = parseLines(ipCidrStr);
			const source_ip_cidr = parseLines(sourceIpCidrStr);
			const source_mac_address = parseLines(sourceMacStr).map((s) => s.toLowerCase());
			const rule_set = ruleSetTags;
			const port = portStr
				.split(',')
				.map((s) => parseInt(s.trim(), 10))
				.filter((n) => !isNaN(n));

			const hasMatcher =
				domain_suffix.length > 0 ||
				ip_cidr.length > 0 ||
				source_ip_cidr.length > 0 ||
				source_mac_address.length > 0 ||
				rule_set.length > 0 ||
				port.length > 0;
			if (!hasMatcher) {
				error = 'Нужен хотя бы один matcher';
				busy = false;
				return;
			}
			if (action === 'route' && !outbound) {
				error = 'Выберите outbound для действия "Направить"';
				busy = false;
				return;
			}

			const src = flat(rule);
			// Матчеры, которых нет в форме, редактор обязан перенести как есть:
			// правило пересобирается с нуля, поэтому всё непоказанное иначе
			// молча пропадает. Так теряются точные домены, прикладной протокол,
			// признак локальной сети и привязка ко входу — из импортированного
			// конфига любое из этого прилетает запросто.
			const carried: SingboxRouterRule = {
				domain: src?.domain?.length ? src.domain : undefined,
				protocol: src?.protocol || undefined,
				ip_is_private: src?.ip_is_private ? true : undefined,
				inbound: src?.inbound?.length ? src.inbound : undefined,
			};

			let built: SingboxRouterRule;
			if (matchersOnly && src) {
				built = {
					...carried,
					domain_suffix: domain_suffix.length ? domain_suffix : undefined,
					ip_cidr: ip_cidr.length ? ip_cidr : undefined,
					action: src.action === 'reject' ? 'reject' : 'route',
					outbound: src.action === 'reject' ? undefined : src.outbound,
				};
			} else {
				built = {
					...carried,
					domain_suffix: domain_suffix.length ? domain_suffix : undefined,
					ip_cidr: ip_cidr.length ? ip_cidr : undefined,
					source_ip_cidr: source_ip_cidr.length ? source_ip_cidr : undefined,
					source_mac_address: source_mac_address.length ? source_mac_address : undefined,
					rule_set: rule_set.length ? rule_set : undefined,
					port: port.length ? port : undefined,
					network: network || undefined,
					action,
					outbound: action === 'route' ? outbound : undefined,
				};
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
	title={matchersOnly ? 'Домены и адреса' : rule ? 'Редактировать правило' : 'Новое правило'}
	onClose={onClose}
	hasUnsavedChanges={() => isDirty}
>
	<div class="form">
		{#if unflattenedLogical}
			<div class="warn">
				Это правило со вложенной логической структурой, которую форма не показывает.
				Сохранение <b>заменит</b> её тем, что введено здесь. Чтобы изменить правило,
				не потеряв структуру, правьте его в редакторе конфигурации.
			</div>
		{/if}

		{#if hiddenMatchers.length}
			<div class="warn">
				В правиле есть условия, которых нет в этой форме — они сохранятся без изменений:
				{#each hiddenMatchers as m, i (m)}<code>{m}</code>{#if i < hiddenMatchers.length - 1}{', '}{/if}{/each}.
				Изменить их можно в экспертном редакторе конфигурации.
			</div>
		{/if}

		<div class="section-label">Matchers (минимум один)</div>

		<label class="field">
			<div class="field-head">
				<span class="lbl">Domain suffix</span>
				{#if domainsCount > 0}
					<span class="count-chip">
						{domainsCount}
						{domainsCount === 1 ? 'домен' : domainsCount < 5 ? 'домена' : 'доменов'}
					</span>
				{/if}
			</div>
			<textarea bind:value={domainSuffixStr} rows="6" placeholder="по одному на строке, например youtube.com"></textarea>
		</label>

		<label class="field">
			<div class="field-head">
				<span class="lbl">IP CIDR</span>
				{#if ipsCount > 0}
					<span class="count-chip">
						{ipsCount}
						{ipsCount === 1 ? 'подсеть' : ipsCount < 5 ? 'подсети' : 'подсетей'}
					</span>
				{/if}
			</div>
			<textarea bind:value={ipCidrStr} rows="6" placeholder="142.250.0.0/15"></textarea>
		</label>

		{#if !matchersOnly}
			<label class="field">
				<div class="field-head">
					<span class="lbl">Source IP CIDR</span>
					{#if sourceIPsCount > 0}
						<span class="count-chip">
							{sourceIPsCount}
							{sourceIPsCount === 1 ? 'источник' : sourceIPsCount < 5 ? 'источника' : 'источников'}
						</span>
					{/if}
				</div>
				<textarea bind:value={sourceIpCidrStr} rows="6" placeholder="192.168.1.50"></textarea>
			</label>

			<label class="field">
				<div class="field-head">
					<span class="lbl">MAC устройства</span>
					{#if sourceMacCount > 0}
						<span class="count-chip">{sourceMacCount}</span>
					{/if}
				</div>
				<textarea bind:value={sourceMacStr} rows="3" placeholder="aa:bb:cc:dd:ee:ff"></textarea>
				<div class="hint">По одному MAC в строке. Устройство определяется по таблице соседей роутера; для устройств за другим роутером не работает.</div>
				{#if pickerOptions.length}
					<Dropdown
						value={pickerValue}
						options={pickerOptions}
						placeholder="Добавить устройство…"
						onchange={pickDevice}
						fullWidth
					/>
				{/if}
			</label>

			<div class="field">
				<div class="lbl">Rule sets</div>
				<ChipMultiSelect
					values={ruleSetTags}
					options={ruleSetOptions}
					onchange={(next) => (ruleSetTags = next)}
					placeholder="не выбрано"
					allowOrphans
				/>
				<div class="hint">
					Готовые наборы (geosite/geoip). Для своих доменов и подсетей используйте поля выше —
					правило сработает по набору <b>или</b> по вашим адресам.
				</div>
			</div>

			<label class="field">
				<div class="lbl">Порты (через запятую)</div>
				<input bind:value={portStr} placeholder="443, 80" />
				<div class="hint">
					Необязательно. Дополнительно ограничивает правило конкретными портами.
				</div>
			</label>

			<div class="field">
				<div class="lbl">Сеть (L4)</div>
				<SegmentedControl
					value={network}
					options={networkOptions}
					ariaLabel="Протокол сети TCP или UDP"
					onchange={(next) => (network = next)}
				/>
				<div class="hint">
					Ограничить правило только TCP или только UDP. «Любой» — без фильтра (как раньше).
				</div>
			</div>

			<div class="action-section">
				<div class="section-label">Действие</div>
				<SegmentedControl
					value={action}
					options={actionOptions}
					ariaLabel="Действие правила маршрутизации"
					onchange={(next) => (action = next)}
				/>

				{#if action === 'route'}
					<label class="field">
						<div class="lbl">Куда направить</div>
						<Dropdown bind:value={outbound} options={outboundDropdownOptions} fullWidth />
					</label>
				{/if}
			</div>
		{/if}

		{#if error}<div class="error">{error}</div>{/if}
	</div>

	{#snippet actions()}
		<Button variant="ghost" size="md" onclick={onClose} type="button">Отмена</Button>
		<Button variant="primary" size="md" onclick={save} disabled={busy} loading={busy} type="button">
			Сохранить
		</Button>
	{/snippet}
</SingboxSettingsModal>
