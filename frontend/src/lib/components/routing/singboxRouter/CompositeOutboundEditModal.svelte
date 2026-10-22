<script lang="ts">
	import { m } from '$lib/i18n';
	import { Button, Dropdown, SegmentedControl, type DropdownOption } from '$lib/components/ui';
	import { X, ChevronUp, ChevronDown } from 'lucide-svelte';
	import type { SegmentedOption } from '$lib/components/ui/segmentedControl';
	import SingboxSettingsModal from './SingboxSettingsModal.svelte';
	import ForeignIfacePanel from './ForeignIfacePanel.svelte';
	import type { SingboxRouterOutbound, SingboxRouterWANInterface } from '$lib/types';
	import { outboundGroupLabel, type OutboundGroup } from './outboundOptions';
	import { isSubscriptionOutbound } from '$lib/components/sb-router/outboundLabel';
	import { subscriptionsStore } from '$lib/stores/subscriptions';
	import { resolveMemberLabel } from '$lib/utils/memberLabel';
	import { api } from '$lib/api/client';
	import { bindInterfaceLabel, directBindChoices } from '$lib/utils/bindInterface';

	// Group types supported: urltest, fallback, load-balance, selector, direct.
	// Sing-box 1.15+ custom binary includes native fallback and load-balance.

	interface Props {
		outbound?: SingboxRouterOutbound;
		/** Outbound'ы того же конфига (tproxy или fakeip) — чьи привязки прятать. */
		outbounds: SingboxRouterOutbound[];
		outboundOptions: OutboundGroup[];
		onClose: () => void;
		onSave: (o: SingboxRouterOutbound) => Promise<void> | void;
	}
	let { outbound, outbounds, outboundOptions, onClose, onSave }: Props = $props();

	type OutboundType = 'urltest' | 'fallback' | 'load-balance' | 'selector' | 'direct';

	// svelte-ignore state_referenced_locally
	let type: OutboundType = $state(
		outbound?.type === 'selector' ? 'selector' :
		outbound?.type === 'direct' ? 'direct' :
		outbound?.type === 'fallback' ? 'fallback' :
		(outbound?.type === 'load-balance' || outbound?.type === 'loadbalance') ? 'load-balance' :
		'urltest'
	);
	// svelte-ignore state_referenced_locally
	let tag = $state(outbound?.tag ?? '');
	// svelte-ignore state_referenced_locally
	let members = $state<string[]>([...(outbound?.outbounds ?? [])]);
	// svelte-ignore state_referenced_locally
	let url = $state(outbound?.url ?? 'https://www.gstatic.com/generate_204');
	// svelte-ignore state_referenced_locally
	let interval = $state(outbound?.interval ?? '3m');
	// svelte-ignore state_referenced_locally
	let tolerance = $state(outbound?.tolerance ?? 50);
	// svelte-ignore state_referenced_locally
	let strategy = $state(outbound?.strategy ?? 'round-robin');
	// svelte-ignore state_referenced_locally
	let defaultOutbound = $state(outbound?.default ?? '');
	// svelte-ignore state_referenced_locally
	let bindInterface = $state(outbound?.bind_interface ?? '');
	let bindables = $state<SingboxRouterWANInterface[]>([]);
	let bindablesLoading = $state(true);
	function loadBindables(): Promise<void> {
		return api.singboxRouterListBindableInterfaces()
			.then((l) => { bindables = l; })
			.catch(() => { bindables = []; })
			.finally(() => { bindablesLoading = false; });
	}
	$effect(() => { void loadBindables(); });
	// До ответа роутера — пусто: заглушка «нет в системе» для своей привязки
	// заслонила бы плейсхолдер «Загрузка интерфейсов…».
	const bindChoices = $derived(bindablesLoading ? [] : directBindChoices(bindables, outbounds, outbound));
	const bindableOptions = $derived<DropdownOption[]>(
		bindChoices.map((i) => ({ value: i.name, label: bindInterfaceLabel(i), group: i.foreign ? m.routing_singbox_outbound_foreign_group() : undefined }))
	);
	async function onForeignPicked(name: string): Promise<void> {
		await loadBindables();
		if (bindChoices.some((i) => i.name === name)) {
			bindInterface = name;
			error = '';
			errorKind = '';
		} else {
			errorKind = 'foreign-missing';
		}
	}

	let busy = $state(false);
	let error = $state('');
	let errorKind = $state<'' | 'foreign-missing' | 'tag' | 'iface' | 'members'>('');
	const errorText = $derived(
		error ||
			(errorKind === 'foreign-missing'
				? m.routing_singbox_outbound_err_foreign_missing()
				: errorKind === 'tag'
					? m.routing_singbox_outbound_err_tag()
					: errorKind === 'iface'
						? m.routing_singbox_outbound_err_iface()
						: errorKind === 'members'
							? m.routing_singbox_outbound_err_members()
							: ''),
	);
	let memberPicker = $state('');

	// Snapshot initial state for isDirty detection
	let initialType: OutboundType = $state('urltest');
	let initialTag = $state('');
	let initialMembers = $state<string[]>([]);
	let initialUrl = $state('https://www.gstatic.com/generate_204');
	let initialInterval = $state('3m');
	let initialTolerance = $state(50);
	let initialStrategy = $state('round-robin');
	let initialDefaultOutbound = $state('');
	let initialBind = $state('');

	// Initialize snapshot when modal opens
	$effect(() => {
		if (outbound) {
			initialType = outbound.type === 'selector' ? 'selector' :
				outbound.type === 'direct' ? 'direct' :
				outbound.type === 'fallback' ? 'fallback' :
				(outbound.type === 'load-balance' || outbound.type === 'loadbalance') ? 'load-balance' :
				'urltest';
			initialTag = outbound.tag;
			initialMembers = [...(outbound.outbounds ?? [])];
			initialUrl = outbound.url ?? 'https://www.gstatic.com/generate_204';
			initialInterval = outbound.interval ?? '3m';
			initialTolerance = outbound.tolerance ?? 50;
			initialStrategy = outbound.strategy ?? 'round-robin';
			initialDefaultOutbound = outbound.default ?? '';
			initialBind = outbound.bind_interface ?? '';
		} else {
			initialType = 'urltest';
			initialTag = '';
			initialMembers = [];
			initialUrl = 'https://www.gstatic.com/generate_204';
			initialInterval = '3m';
			initialTolerance = 50;
			initialStrategy = 'round-robin';
			initialDefaultOutbound = '';
			initialBind = '';
		}
	});

	const subsData = $derived($subscriptionsStore?.data ?? []);
	const isSubscription = $derived(
		outbound ? isSubscriptionOutbound(outbound, subsData) : false,
	);

	const isDirty = $derived.by(() => {
		const membersChanged =
			!isSubscription && [...members].join(',') !== [...initialMembers].join(',');
		return (
			type !== initialType ||
			tag !== initialTag ||
			membersChanged ||
			url !== initialUrl ||
			interval !== initialInterval ||
			tolerance !== initialTolerance ||
			strategy !== initialStrategy ||
			defaultOutbound !== initialDefaultOutbound ||
			bindInterface !== initialBind
		);
	});

	// Flat options with group labels for the Dropdown native grouping.
	// Filter out tags already added so the user can't pick duplicates, and
	// the group's own tag so it can't reference itself (self-reference
	// FATALs sing-box with a circular-dependency error).
	const memberDropdownOptions = $derived<DropdownOption[]>(
		outboundOptions.flatMap((g) =>
			g.items
				.filter((i) => !members.includes(i.value) && i.value !== tag.trim())
				.map((i) => ({ value: i.value, label: i.label, group: outboundGroupLabel(g.id) }))
		)
	);

	// Advisory: warn when the group's tag matches an existing outbound's
	// display name (e.g. a composite named "DE" like the AWG tunnel "DE").
	const tagCollision = $derived(
		tag.trim() !== '' &&
			outboundOptions.some((g) => g.items.some((i) => i.value === tag.trim() || i.label === tag.trim()))
	);

	// Default candidate options: only current members of this group.
	const defaultOptions = $derived<DropdownOption[]>(
		members.map((m) => ({ value: m, label: memberLabel(m) }))
	);

	const strategyOptions: DropdownOption[] = [
		{ value: 'round-robin', label: 'Round-robin (по кругу)' },
		{ value: 'consistent-hashing', label: 'Consistent hashing (по IP назначения)' }
	];

	function memberLabel(tagOrName: string): string {
		return resolveMemberLabel(tagOrName, outboundOptions);
	}

	function addMember(v: string): void {
		if (!v || isSubscription) return;
		if (!members.includes(v)) {
			members = [...members, v];
			if (!defaultOutbound) defaultOutbound = v;
		}
		memberPicker = '';
	}

	function moveMember(index: number, direction: -1 | 1): void {
		if (isSubscription) return;
		const target = index + direction;
		if (target < 0 || target >= members.length) return;
		const next = [...members];
		const temp = next[index];
		next[index] = next[target];
		next[target] = temp;
		members = next;
	}

	function removeMember(v: string): void {
		if (isSubscription) return;
		members = members.filter((mem) => mem !== v);
		if (defaultOutbound === v) defaultOutbound = '';
	}

	async function save(): Promise<void> {
		busy = true;
		error = '';
		errorKind = '';
		try {
			if (!tag.trim()) {
				errorKind = 'tag';
				busy = false;
				return;
			}

			let built: SingboxRouterOutbound;
			if (type === 'direct') {
				if (!bindInterface) {
					errorKind = 'iface';
					busy = false;
					return;
				}
				built = { type: 'direct', tag: tag.trim(), bind_interface: bindInterface };
			} else {
				const memberList = isSubscription && outbound ? [...(outbound.outbounds ?? [])] : [...members];
				if (memberList.length < 2) {
					errorKind = 'members';
					busy = false;
					return;
				}
				built = {
					type,
					tag: tag.trim(),
					outbounds: memberList,
				};
				if (outbound?.source === 'subscription') {
					built.source = 'subscription';
				}
				if (type === 'urltest') {
					built.url = url;
					built.interval = interval;
					built.tolerance = tolerance;
				} else if (type === 'fallback') {
					built.url = url;
					built.interval = interval;
					built.interrupt_exist_connections = true;
				} else if (type === 'load-balance') {
					built.url = url;
					built.interval = interval;
					built.strategy = strategy;
				} else {
					built.default = defaultOutbound || members[0];
				}
			}

			await onSave(built);
		} catch (e) {
			error = (e as Error).message;
		} finally {
			busy = false;
		}
	}

	const typeOptions = $derived<SegmentedOption<OutboundType>[]>([
		{ value: 'urltest', label: 'URLTest', disabled: !!outbound && outbound.type === 'direct' },
		{ value: 'fallback', label: 'Fallback', disabled: !!outbound && outbound.type === 'direct' },
		{ value: 'load-balance', label: 'LoadBalance', disabled: !!outbound && outbound.type === 'direct' },
		{ value: 'selector', label: 'Selector', disabled: !!outbound && outbound.type === 'direct' },
		{ value: 'direct', label: m.routing_singbox_outbound_type_interface(), disabled: !!outbound && outbound.type !== 'direct' },
	]);

	const typeDescription = $derived(
		type === 'direct'
			? m.routing_singbox_outbound_desc_direct()
			: type === 'urltest'
				? m.routing_singbox_outbound_desc_urltest()
				: type === 'fallback'
					? m.routing_singbox_outbound_desc_fallback()
					: type === 'load-balance'
						? m.routing_singbox_outbound_desc_loadbalance()
						: m.routing_singbox_outbound_desc_selector()
	);
</script>

<SingboxSettingsModal
	title={outbound ? m.routing_singbox_outbound_edit_title() : m.routing_singbox_outbound_new_title()}
	onClose={onClose}
	size="lg"
	hasUnsavedChanges={() => isDirty}
>
	<div class="form">
		<div class="field type-field">
			<div class="lbl">{m.routing_singbox_outbound_type()}</div>
			<SegmentedControl
				value={type}
				options={typeOptions}
				ariaLabel={m.routing_singbox_outbound_type_aria()}
				onchange={(next) => (type = next)}
			/>
			<div class="type-hint">{typeDescription}</div>
		</div>

		<label class="field">
			<div class="lbl">{m.routing_singbox_outbound_tag_label()}</div>
			<input bind:value={tag} placeholder="fast-de" />
			{#if tagCollision}
				<div class="tag-warn">{m.routing_singbox_outbound_tag_collision()}</div>
			{/if}
		</label>

		{#if type !== 'direct'}
			<div class="field">
				<div class="lbl">{isSubscription ? m.routing_singbox_outbound_members_sub() : m.routing_singbox_outbound_members_min()}</div>	
				<div class="member-chips" class:empty={members.length === 0}>
					{#if members.length === 0}
						<span class="chips-placeholder">{m.routing_singbox_outbound_members_none()}</span>
					{:else}
						{#each members as mem, i (mem)}
							<span class="member-chip" title={mem}>
								{#if type !== 'load-balance'}
									<span class="member-chip-order" class:primary={i === 0}>#{i + 1}</span>
								{/if}
								<span class="member-chip-label">{memberLabel(mem)}</span>
								{#if !isSubscription}
									<div class="member-chip-actions">
										<div class="member-chip-nav-col">
											{#if i > 0}
												<button
													type="button"
													class="member-chip-nav"
													aria-label="Move up"
													title={type === 'load-balance' ? 'Переместить раньше' : 'Поднять выше (к #1)'}
													onclick={() => moveMember(i, -1)}
												>
													<ChevronUp size={11} />
												</button>
											{/if}
											{#if i < members.length - 1}
												<button
													type="button"
													class="member-chip-nav"
													aria-label="Move down"
													title={type === 'load-balance' ? 'Переместить позже' : 'Опустить ниже'}
													onclick={() => moveMember(i, 1)}
												>
													<ChevronDown size={11} />
												</button>
											{/if}
										</div>
										<button
											type="button"
											class="member-chip-remove"
											aria-label={m.routing_singbox_outbound_remove_aria({ name: mem })}
											title={m.routing_singbox_outbound_remove()}
											onclick={() => removeMember(mem)}
										>
											<X size={14} strokeWidth={2.5} aria-hidden="true" />
										</button>
									</div>
								{/if}
							</span>
						{/each}
					{/if}
				</div>
				{#if isSubscription}
					<div class="type-hint">{m.routing_singbox_outbound_sub_hint()}</div>
				{/if}
				{#if !isSubscription}
					<Dropdown
						value={memberPicker}
						options={memberDropdownOptions}
						placeholder={m.routing_singbox_outbound_add_member()}
						onchange={addMember}
						fullWidth
					/>
				{/if}
			</div>

			{#if type === 'urltest'}
				<label class="field">
					<div class="lbl">Test URL</div>
					<input bind:value={url} />
				</label>
				<div class="row2">
					<label class="field">
						<div class="lbl">Interval</div>
						<input bind:value={interval} placeholder="3m" />
					</label>
					<label class="field">
						<div class="lbl">Tolerance (ms)</div>
						<input type="number" bind:value={tolerance} />
					</label>
				</div>
			{:else if type === 'fallback'}
				<label class="field">
					<div class="lbl">Test URL</div>
					<input bind:value={url} />
				</label>
				<label class="field">
					<div class="lbl">Interval</div>
					<input bind:value={interval} placeholder="30s" />
				</label>
			{:else if type === 'load-balance'}
				<div class="field">
					<div class="lbl">Стратегия балансировки</div>
					<Dropdown
						bind:value={strategy}
						options={strategyOptions}
						fullWidth
					/>
				</div>
				<label class="field">
					<div class="lbl">Test URL</div>
					<input bind:value={url} />
				</label>
				<label class="field">
					<div class="lbl">Interval</div>
					<input bind:value={interval} placeholder="3m" />
				</label>
			{:else if type === 'selector'}
				<div class="field">
					<div class="lbl">{m.routing_singbox_outbound_default_label()}</div>
					<Dropdown
						bind:value={defaultOutbound}
						options={defaultOptions}
						placeholder={members.length === 0 ? m.routing_singbox_outbound_add_members_first() : m.routing_singbox_outbound_choose()}
						disabled={members.length === 0}
						fullWidth
					/>
				</div>
			{/if}
		{/if}

		{#if type === 'direct'}
			<div class="field">
				<div class="lbl">{m.routing_singbox_outbound_type_interface()}</div>
				<Dropdown
					bind:value={bindInterface}
					options={bindableOptions}
					placeholder={bindablesLoading ? m.routing_singbox_outbound_loading_ifaces() : bindChoices.length === 0 ? m.routing_singbox_outbound_no_ifaces() : m.routing_singbox_outbound_choose_iface()}
					disabled={bindablesLoading || bindChoices.length === 0}
					fullWidth
				/>
				<ForeignIfacePanel onpicked={onForeignPicked} />
			</div>
		{/if}

		{#if errorText}<div class="error">{errorText}</div>{/if}
	</div>

	{#snippet actions()}
		<Button variant="ghost" size="md" onclick={onClose} type="button">{m.common_cancel()}</Button>
		<Button variant="primary" size="md" onclick={save} disabled={busy} loading={busy} type="button">
			{m.common_save()}
		</Button>
	{/snippet}
</SingboxSettingsModal>
