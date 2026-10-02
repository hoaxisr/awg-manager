<!--
  Пресет DNS-цепочки (sing-box 1.14 evaluate/match_response). Собирает цепочку
  бэкенд — здесь только режим, два сервера и список «отравленных» ответов.
  Только SlotRouter: в режиме FakeIP цепочку писать некуда, карточка залочена.
-->
<script lang="ts">
	import { m } from '$lib/i18n';
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

	const MODE_OPTIONS = $derived<SegmentedOption<SingboxRouterDNSChainMode>[]>([
		{ value: '', label: m.routing_singbox_chain_off() },
		{ value: 'resilient', label: m.routing_singbox_chain_resilient() },
		{ value: 'antipoison', label: m.routing_singbox_chain_antipoison() },
	]);

	const POISON_PLACEHOLDER = '0.0.0.0/32\n127.0.0.0/8\n10.10.34.34/32\n10.10.34.35/32';

	// fakeip-серверы не резолвят — evaluate на них бэкенд отклоняет.
	const serverOptions = $derived<DropdownOption[]>(
		servers
			.filter((s) => s.type !== 'fakeip')
			.map((s) => ({
				value: s.tag,
				label: s.tag,
				description: s.detour ? m.routing_singbox_chain_via({ detour: s.detour }) : undefined,
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
			? m.routing_singbox_chain_fakeip_unavailable_rules()
			: m.routing_singbox_chain_fakeip_unavailable(),
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
	<div class="cap">{m.routing_singbox_chain_title()}</div>

	<SegmentedControl
		value={mode}
		options={MODE_OPTIONS}
		ariaLabel={m.routing_singbox_chain_mode_aria()}
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
			label={m.routing_singbox_chain_direct_dns()}
			placeholder={m.routing_singbox_chain_choose()}
			fullWidth
		/>
		<Dropdown
			bind:value={proxyServer}
			options={serverOptions}
			label={m.routing_singbox_chain_tunnel_dns()}
			placeholder={m.routing_singbox_chain_choose()}
			fullWidth
		/>
		{#if mode === 'antipoison'}
			<label class="field">
				<div class="lbl">{m.routing_singbox_chain_suspicious_ips()}</div>
				<textarea class="inp" rows="4" placeholder={POISON_PLACEHOLDER} bind:value={poisonText}
				></textarea>
			</label>
		{/if}
		<p class="hint">
			{m.routing_singbox_chain_order_hint()}
		</p>
	{/if}

	{#if shadowed}
		<p class="warn">
			{m.routing_singbox_chain_shadowed()}
		</p>
	{/if}

	<p class="hint">
		{m.routing_singbox_chain_final_hint({ server: finalServer || '—' })}
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
		{m.routing_singbox_apply()}
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
