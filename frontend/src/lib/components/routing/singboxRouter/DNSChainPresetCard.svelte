<!--
  Пресет DNS-цепочки (sing-box 1.14 evaluate/match_response). Собирает цепочку
  бэкенд — здесь только режим, два сервера и список «отравленных» ответов.
  Только SlotRouter: в режиме FakeIP цепочку писать некуда, карточка залочена.
-->
<script lang="ts">
	import {
		Button,
		Dropdown,
		SegmentedControl,
		type DropdownOption,
		type SegmentedOption,
	} from '$lib/components/ui';
	import type {
		SingboxRouterDNSChainMode,
		SingboxRouterDNSChainPreset,
		SingboxRouterDNSRule,
		SingboxRouterDNSServer,
	} from '$lib/types';
	import {
		isDnsChainShadowed,
		isManagedDnsChainRule,
	} from '$lib/components/sb-router/dnsChainManaged';

	interface Props {
		servers: SingboxRouterDNSServer[];
		/** Текущий список DNS-правил — нужен, чтобы увидеть цепочку в нём. */
		rules?: SingboxRouterDNSRule[];
		preset: SingboxRouterDNSChainPreset;
		/** dns.final — сервер, куда уходит запрос, если цепочка не ответила. */
		finalServer: string;
		fakeipMode?: boolean;
		onApply: (preset: SingboxRouterDNSChainPreset) => Promise<void> | void;
	}

	let { servers, rules = [], preset, finalServer, fakeipMode = false, onApply }: Props = $props();

	const MODE_OPTIONS: SegmentedOption<SingboxRouterDNSChainMode>[] = [
		{ value: '', label: 'Выкл' },
		{ value: 'resilient', label: 'Отказоустойчивый' },
		{ value: 'antipoison', label: 'Анти-подмена' },
	];

	const POISON_PLACEHOLDER = '0.0.0.0/32\n127.0.0.0/8\n10.10.34.34/32\n10.10.34.35/32';

	// fakeip-серверы не резолвят — evaluate на них бэкенд отклоняет.
	const serverOptions = $derived<DropdownOption[]>(
		servers
			.filter((s) => s.type !== 'fakeip')
			.map((s) => ({
				value: s.tag,
				label: s.tag,
				description: s.detour ? `через ${s.detour}` : undefined,
			})),
	);

	let mode = $state<SingboxRouterDNSChainMode>('');
	let directServer = $state('');
	let proxyServer = $state('');
	let poisonText = $state('');

	$effect(() => {
		mode = preset.mode;
		directServer = preset.directServer ?? '';
		proxyServer = preset.proxyServer ?? '';
		poisonText = (preset.poisonCidrs ?? []).join('\n');
	});

	let busy = $state(false);
	let error = $state('');

	const incomplete = $derived(mode !== '' && (!directServer || !proxyServer));

	// Цепочка уже применена, но перекрыта пользовательским catch-all выше.
	const shadowed = $derived(preset.mode !== '' && isDnsChainShadowed(rules));
	// В fakeip-режиме цепочка не пишется, но старые managed-правила в списке
	// остаются — они относятся к TPROXY и сейчас ничего не делают.
	const fakeipHint = $derived(
		rules.some(isManagedDnsChainRule)
			? 'Недоступно в режиме FakeIP. Правила цепочки в списке относятся к режиму TPROXY и сейчас неактивны.'
			: 'Недоступно в режиме FakeIP',
	);

	async function apply(): Promise<void> {
		if (busy || fakeipMode || incomplete) return;
		const cidrs = poisonText
			.split('\n')
			.map((s) => s.trim())
			.filter(Boolean);
		const next: SingboxRouterDNSChainPreset =
			mode === ''
				? { mode: '' }
				: {
						mode,
						directServer,
						proxyServer,
						...(mode === 'antipoison' && cidrs.length > 0 ? { poisonCidrs: cidrs } : {}),
					};
		busy = true;
		error = '';
		try {
			await onApply(next);
		} catch (e) {
			error = e instanceof Error ? e.message : String(e);
		} finally {
			busy = false;
		}
	}
</script>

<section class="preset-card">
	<div class="cap">Пресет DNS-цепочки</div>

	<SegmentedControl
		value={mode}
		options={MODE_OPTIONS}
		ariaLabel="Режим DNS-пресета"
		disabled={fakeipMode}
		fullWidth
		onchange={(v) => (mode = v)}
	/>

	{#if fakeipMode}
		<p class="hint">{fakeipHint}</p>
	{:else if mode !== ''}
		<Dropdown
			bind:value={directServer}
			options={serverOptions}
			label="Прямой DNS"
			placeholder="— выбрать —"
			fullWidth
		/>
		<Dropdown
			bind:value={proxyServer}
			options={serverOptions}
			label="DNS через туннель"
			placeholder="— выбрать —"
			fullWidth
		/>
		{#if mode === 'antipoison'}
			<label class="field">
				<div class="lbl">Подозрительные IP (CIDR, по строке)</div>
				<textarea class="inp" rows="4" placeholder={POISON_PLACEHOLDER} bind:value={poisonText}
				></textarea>
			</label>
		{/if}
		<p class="hint">
			Правила пресета выполняются после ваших: цепочка автоматически держится в конце списка
		</p>
	{/if}

	{#if shadowed}
		<p class="warn">
			Пресет не действует: выше цепочки стоит catch-all-правило, перехватывающее все запросы
		</p>
	{/if}

	<p class="hint">
		Если оба резолвера недоступны, запрос уходит на финальный сервер: {finalServer || '—'}
	</p>

	{#if error}<p class="err">{error}</p>{/if}

	<Button
		variant="primary"
		size="sm"
		fullWidth
		disabled={busy || fakeipMode || incomplete}
		loading={busy}
		onclick={apply}
	>
		Применить
	</Button>
</section>

<style>
	.preset-card {
		display: flex;
		flex-direction: column;
		min-width: 0;
		gap: 8px;
		padding: 12px 14px;
		border-bottom: 1px solid var(--border);
	}
	.cap {
		font-size: 11px;
		font-weight: 600;
		text-transform: uppercase;
		letter-spacing: 0.05em;
		color: var(--text-muted);
	}
	.hint {
		margin: 0;
		font-size: 11.5px;
		color: var(--text-muted);
		line-height: 1.4;
	}
	.warn {
		margin: 0;
		font-size: 11.5px;
		color: var(--color-warning, #d97706);
		line-height: 1.4;
	}
	.err {
		margin: 0;
		font-size: 11.5px;
		color: var(--error);
		line-height: 1.4;
	}
	.field {
		display: flex;
		flex-direction: column;
		min-width: 0;
		gap: 4px;
	}
	.lbl {
		font-size: 13px;
		color: var(--text-secondary);
		font-weight: 500;
	}
	.inp {
		width: 100%;
		min-width: 0;
		box-sizing: border-box;
		padding: 6px 8px;
		border-radius: var(--radius-sm);
		background: var(--bg-primary);
		border: 1px solid var(--border);
		color: var(--text-primary);
		font-family: var(--font-mono);
		font-size: 12px;
		resize: vertical;
	}
</style>
