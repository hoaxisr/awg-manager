<script lang="ts">
	import { Button, Dropdown, SegmentedControl, type DropdownOption } from '$lib/components/ui';
	import { X } from 'lucide-svelte';
	import type { SegmentedOption } from '$lib/components/ui/segmentedControl';
	import SingboxSettingsModal from './SingboxSettingsModal.svelte';
	import type { SingboxRouterOutbound, SingboxRouterWANInterface } from '$lib/types';
	import type { OutboundGroup } from './outboundOptions';
	import { isSubscriptionOutbound } from '$lib/components/sb-router/outboundLabel';
	import { subscriptionsStore } from '$lib/stores/subscriptions';
	import { resolveMemberLabel } from '$lib/utils/memberLabel';
	import { api } from '$lib/api/client';

	// Only urltest and selector are offered for new groups — `loadbalance`
	// was removed in sing-box 1.13+ and FATALs on startup if present. Legacy
	// loadbalance entries that may exist in older 20-router.json files are
	// still tolerated at read time. When the user edits such an entry through
	// this modal, the type narrows to urltest on open — that's a deliberate
	// one-way migration, not an accidental data loss.

	interface Props {
		outbound?: SingboxRouterOutbound;
		outboundOptions: OutboundGroup[];
		onClose: () => void;
		onSave: (o: SingboxRouterOutbound) => Promise<void> | void;
	}
	let { outbound, outboundOptions, onClose, onSave }: Props = $props();

	// svelte-ignore state_referenced_locally
	let type: 'urltest' | 'selector' | 'direct' = $state(
		outbound?.type === 'selector' ? 'selector' : outbound?.type === 'direct' ? 'direct' : 'urltest'
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
	let defaultOutbound = $state(outbound?.default ?? '');
	// svelte-ignore state_referenced_locally
	let bindInterface = $state(outbound?.bind_interface ?? '');
	let bindables = $state<SingboxRouterWANInterface[]>([]);
	let bindablesLoading = $state(true);
	$effect(() => {
		void api.singboxRouterListBindableInterfaces()
			.then((l) => { bindables = l; })
			.catch(() => { bindables = []; })
			.finally(() => { bindablesLoading = false; });
	});
	const bindableOptions = $derived<DropdownOption[]>(
		bindables.map((i) => ({ value: i.name, label: `${i.label} · ${i.name}${i.up ? '' : ' (down)'}` }))
	);

	let busy = $state(false);
	let error = $state('');
	let memberPicker = $state('');

	// Snapshot initial state for isDirty detection
	let initialType: 'urltest' | 'selector' | 'direct' = $state('urltest');
	let initialTag = $state('');
	let initialMembers = $state<string[]>([]);
	let initialUrl = $state('https://www.gstatic.com/generate_204');
	let initialInterval = $state('3m');
	let initialTolerance = $state(50);
	let initialDefaultOutbound = $state('');
	let initialBind = $state('');

	// Initialize snapshot when modal opens
	$effect(() => {
		if (outbound) {
			initialType = outbound.type === 'selector' ? 'selector' : outbound.type === 'direct' ? 'direct' : 'urltest';
			initialTag = outbound.tag;
			initialMembers = [...(outbound.outbounds ?? [])];
			initialUrl = outbound.url ?? 'https://www.gstatic.com/generate_204';
			initialInterval = outbound.interval ?? '3m';
			initialTolerance = outbound.tolerance ?? 50;
			initialDefaultOutbound = outbound.default ?? '';
			initialBind = outbound.bind_interface ?? '';
		} else {
			initialType = 'urltest';
			initialTag = '';
			initialMembers = [];
			initialUrl = 'https://www.gstatic.com/generate_204';
			initialInterval = '3m';
			initialTolerance = 50;
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
				.map((i) => ({ value: i.value, label: i.label, group: g.group }))
		)
	);

	// Advisory: warn when the group's tag matches an existing outbound's
	// display name (e.g. a composite named "DE" like the AWG tunnel "DE").
	// Tags are the real identifier, so this is not an error — but the name
	// clash is exactly what leads users to add the wrong "DE" as a member.
	const tagCollision = $derived.by(() => {
		const t = tag.trim();
		if (!t) return false;
		return outboundOptions.some((g) =>
			g.items.some((i) => i.value !== t && i.label.replace(/\s*\(.*\)\s*$/, '') === t)
		);
	});

	// Default-picker options: only members already chosen. Подписочные
	// тэги (sub-XXX-YYY) и awg-XXX тэги резолвим в человеческие labels —
	// тот же UX, что и на карточке composite outbound (issue #214).
	function memberLabel(tag: string): string {
		return resolveMemberLabel(tag, subsData, outboundOptions);
	}
	const defaultOptions = $derived<DropdownOption[]>(
		members.map((m) => ({ value: m, label: memberLabel(m) }))
	);

	function addMember(v: string): void {
		if (isSubscription) return;
		if (!v) return;
		if (members.includes(v)) return;
		members = [...members, v];
		// Reset picker so the same slot can be reused for the next addition.
		memberPicker = '';
	}

	function removeMember(v: string): void {
		if (isSubscription) return;
		members = members.filter((m) => m !== v);
		if (defaultOutbound === v) defaultOutbound = '';
	}

	async function save(): Promise<void> {
		busy = true;
		error = '';
		try {
			if (!tag.trim()) {
				error = 'Tag обязателен';
				busy = false;
				return;
			}

			let built: SingboxRouterOutbound;
			if (type === 'direct') {
				if (!bindInterface) {
					error = 'Выберите интерфейс';
					busy = false;
					return;
				}
				built = { type: 'direct', tag: tag.trim(), bind_interface: bindInterface };
			} else {
				const memberList = isSubscription && outbound ? [...(outbound.outbounds ?? [])] : [...members];
				if (memberList.length < 2) {
					error = 'Нужно минимум 2 члена';
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

	type OutboundType = 'urltest' | 'selector' | 'direct';

	const typeOptions = $derived<SegmentedOption<OutboundType>[]>([
		{ value: 'urltest', label: 'URLTest', disabled: !!outbound && outbound.type === 'direct' },
		{ value: 'selector', label: 'Selector', disabled: !!outbound && outbound.type === 'direct' },
		{ value: 'direct', label: 'Интерфейс', disabled: !!outbound && outbound.type !== 'direct' },
	]);

	const typeDescription = $derived(
		type === 'direct'
			? 'Прямое соединение через выбранный интерфейс (IPSec/IKEv2/др. VPN). Трафик правила пойдёт через этот интерфейс.'
			: type === 'urltest'
				? 'Периодически пингует каждого члена и автоматически направляет через самого быстрого.'
				: 'Ручное переключение через Clash API. Новые подключения идут через выбранный default.'
	);
</script>

<SingboxSettingsModal
	title={outbound ? 'Редактировать outbound' : 'Новый outbound'}
	onClose={onClose}
	size="lg"
	hasUnsavedChanges={() => isDirty}
>
	<div class="form">
		<div class="field type-field">
			<div class="lbl">Тип</div>
			<SegmentedControl
				value={type}
				options={typeOptions}
				ariaLabel="Тип outbound"
				onchange={(next) => (type = next)}
			/>
			<div class="type-hint">{typeDescription}</div>
		</div>

		<label class="field">
			<div class="lbl">Tag (имя)</div>
			<input bind:value={tag} placeholder="fast-de" />
			{#if tagCollision}
				<div class="tag-warn">⚠ Имя совпадает с именем существующего туннеля — их легко перепутать в списке участников. Лучше дать группе отличающееся имя.</div>
			{/if}
		</label>

		{#if type !== 'direct'}
			<div class="field">
				<div class="lbl">{isSubscription ? 'Участники' : 'Members (минимум 2)'}</div>	
				<div class="member-chips" class:empty={members.length === 0}>
					{#if members.length === 0}
						<span class="chips-placeholder">Участники не выбраны</span>
					{:else}
						{#each members as m (m)}
							<span class="member-chip" title={m}>
								<span class="member-chip-label">{memberLabel(m)}</span>
								{#if !isSubscription}
									<button
										type="button"
										class="member-chip-remove"
										aria-label={`Удалить ${m}`}
										title="Удалить"
										onclick={() => removeMember(m)}
									>
										<X size={14} strokeWidth={2.5} aria-hidden="true" />
									</button>
								{/if}
							</span>
						{/each}
					{/if}
				</div>
				{#if isSubscription}
					<div class="type-hint">Список редактируется в разделе «Туннели» -> «Sing-box подписки»</div>
				{/if}
				{#if !isSubscription}
					<Dropdown
						value={memberPicker}
						options={memberDropdownOptions}
						placeholder="Добавить участника"
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
			{:else}
				<div class="field">
					<div class="lbl">Default (один из members)</div>
					<Dropdown
						bind:value={defaultOutbound}
						options={defaultOptions}
						placeholder={members.length === 0 ? 'Сначала добавьте участников' : '— выбрать —'}
						disabled={members.length === 0}
						fullWidth
					/>
				</div>
			{/if}
		{/if}

		{#if type === 'direct'}
			<div class="field">
				<div class="lbl">Интерфейс</div>
				<Dropdown
					bind:value={bindInterface}
					options={bindableOptions}
					placeholder={bindablesLoading ? 'Загрузка интерфейсов…' : bindables.length === 0 ? 'Нет доступных интерфейсов' : '— выбрать интерфейс —'}
					disabled={bindablesLoading || bindables.length === 0}
					fullWidth
				/>
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
