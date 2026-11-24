<script lang="ts">
	import { m } from '$lib/i18n';
	import { Badge, Button, FieldHint } from '$lib/components/ui';
	import { ExternalLink } from 'lucide-svelte';
	import { findPolicyForInterface } from '$lib/utils/accessPolicy';
	import type { AccessPolicy, TunnelListItem } from '$lib/types';
	import DetailSection from './DetailSection.svelte';

	interface Props {
		protocol: 'wdtt' | 'freeturn';
		raw: boolean;
		rawNdms: string;
		rawKernel: string;
		tunnel: TunnelListItem | null;
		port?: string | null;
		policies: AccessPolicy[];
	}

	let { protocol, raw, rawNdms, rawKernel, tunnel, port, policies }: Props = $props();

	const policyIface = $derived(raw ? rawNdms : (tunnel?.ndmsName ?? ''));
	const policy = $derived(policyIface ? findPolicyForInterface(policies, policyIface) : null);
	// Имя политики — как на «Маршрутизации» (PolicyTable.svelte): Policy0 пользователю не показывают.
	const policyLabel = $derived(policy ? policy.description || policy.name : '');

	const rawHint = $derived(
		rawKernel
			? m.proxy_exit_detail_raw_hint_kernel({ kernel: rawKernel })
			: m.proxy_exit_detail_raw_hint(),
	);
</script>

<!-- Секции нет, пока интерфейс клиента неизвестен: пустой заголовок ничего
     не сообщает, а обещать «не заведён в политику» не о чем. -->
{#if policyIface}
	<DetailSection title={m.proxy_exit_detail_where_title()}>
		{#if raw}
			<div class="line-row">
				<span class="line-label">{m.proxy_exit_detail_in_router()}</span>
				<code>{rawNdms}</code>
				<FieldHint text={rawHint} ariaLabel={m.proxy_exit_detail_iface_aria()} />
			</div>
		{:else if tunnel}
			<!-- EX-09 — и у WDTT-WG, и у FreeTurn-клиента: туннель есть у обоих.
			     (i) EX-10 рассказывает про режим WG — он только у WDTT. -->
			<div class="line-row">
				<span class="line-label">{m.proxy_exit_detail_awg_tunnel()}</span>
				<a class="link" href={`/tunnels/${tunnel.id}`}>{tunnel.name}<ExternalLink size={12} /></a>
				{#if protocol === 'wdtt'}
					<FieldHint
						text={m.proxy_exit_detail_wg_hint({ port: port ?? '' })}
						ariaLabel={m.proxy_exit_detail_awg_aria()}
					/>
				{/if}
			</div>
		{/if}
		<div class="line-row">
			<span class="line-label">{m.proxy_exit_detail_policy()}</span>
			{#if policyLabel}
				<Badge size="sm" variant="success">{policyLabel}</Badge>
			{:else}
				<Badge size="sm" variant="muted">{m.proxy_exit_detail_no_policy()}</Badge>
			{/if}
			<FieldHint
				text={m.proxy_exit_wizard_policy_hint()}
				ariaLabel={m.proxy_exit_wizard_policy_aria()}
			/>
			<Button variant="ghost" size="sm" href="/routing">
				{m.proxy_exit_wizard_routing()}
				{#snippet iconAfter()}<ExternalLink size={12} />{/snippet}
			</Button>
		</div>
	</DetailSection>
{/if}

<style>
	.line-row {
		display: flex;
		align-items: center;
		gap: 0.5rem;
		flex-wrap: wrap;
		margin-bottom: 0.375rem;
		font-size: 0.8125rem;
		color: var(--color-text-secondary);
	}

	.line-label {
		color: var(--color-text-muted);
	}

	.link {
		display: inline-flex;
		align-items: center;
		gap: 0.25rem;
		color: var(--color-accent);
		font-size: 0.8125rem;
		text-decoration: none;
	}

	.link:hover {
		text-decoration: underline;
	}

	code {
		font-family: var(--font-mono);
		font-size: 0.78em;
		background: var(--color-bg-tertiary);
		padding: 0.05rem 0.3rem;
		border-radius: 4px;
		word-break: break-all;
	}
</style>
