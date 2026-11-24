<script lang="ts">
	// Доступные способы подключения: прямой MTProto и веб-мост через домен.
	import { m } from '$lib/i18n';
	import type { TgWebProxyConfig, TgWebProxyStatus } from '$lib/types';
	import { EXTERNAL_ACCESS_LABELS } from '../external-access/connectionLabels';
	import { formatDomainList } from '../external-access/externalAccessModel';
	import { cleanEndpointDescription } from './tgProxyModel';
	import { Smartphone, Globe } from 'lucide-svelte';

	interface Props {
		config: TgWebProxyConfig | null;
		status: TgWebProxyStatus | null;
		expectsMt: boolean;
		expectsWeb: boolean;
	}

	let { config, status, expectsMt, expectsWeb }: Props = $props();
</script>

<section class="methods-grid">
	{#if expectsMt}
		<div class="method-card">
			<div class="method-top">
				<div class="method-title-wrap">
					<Smartphone size={15} class="method-icon" />
					<span class="method-title">{EXTERNAL_ACCESS_LABELS.telegramMtproto}</span>
				</div>
				<span class="method-status" class:online={status?.direct_online}>
					{status?.direct_online ? m.servers_tg_method_ready() : m.servers_tg_method_off()}
				</span>
			</div>
			<p class="method-desc">
				{m.servers_tg_method_mtproto_desc()}
			</p>
			<div class="method-footer">
				<span class="method-addr font-mono">
					{config?.direct_host ? `${config.direct_host}:${config?.direct_port || 8443}` : m.servers_tg_method_no_address()}
				</span>
			</div>
		</div>
	{/if}

	{#if expectsWeb}
		<div class="method-card">
			<div class="method-top">
				<div class="method-title-wrap">
					<Globe size={15} class="method-icon" />
					<span class="method-title">{EXTERNAL_ACCESS_LABELS.telegramWeb}</span>
				</div>
				<span class="method-status" class:online={status?.webproxy_online}>
					{status?.webproxy_online ? m.servers_tg_method_ready() : m.servers_tg_method_off()}
				</span>
			</div>
			<p class="method-desc">
				{m.servers_tg_method_web_desc()}
			</p>
			{#if status?.web_proxies && status.web_proxies.length > 0}
				<div class="method-domains">
					{#each status.web_proxies as wp (wp.id)}
						<div class="domain-row">
							<div class="domain-info">
								<span class="domain-host font-mono">{wp.hostname}</span>
								{#if wp.description}
									<span class="domain-desc">{cleanEndpointDescription(wp.description)}</span>
								{/if}
							</div>
						</div>
					{/each}
				</div>
			{:else}
				<div class="method-footer">
					<span class="method-addr font-mono">
						{formatDomainList(config?.public_hostname) || m.servers_tg_method_no_domain()}
					</span>
				</div>
			{/if}
		</div>
	{/if}
</section>

<style>
	.methods-grid {
		display: grid;
		grid-template-columns: repeat(auto-fit, minmax(260px, 1fr));
		gap: 10px;
	}

	.method-card {
		padding: 11px 13px;
		background: var(--color-bg-primary);
		border: 1px solid var(--color-border);
		border-radius: var(--radius-sm);
		display: flex;
		flex-direction: column;
		justify-content: space-between;
		gap: 8px;
	}

	.method-top {
		display: flex;
		align-items: center;
		justify-content: space-between;
		gap: 8px;
		flex-wrap: wrap;
	}

	.method-title-wrap {
		display: flex;
		align-items: center;
		gap: 7px;
	}

	:global(.method-icon) {
		color: var(--color-accent);
		flex-shrink: 0;
	}

	.method-title {
		font-size: 13px;
		font-weight: 600;
		color: var(--color-text-primary);
	}

	.method-status {
		font-size: 10.5px;
		padding: 1px 7px;
		border-radius: 4px;
		background: var(--color-bg-secondary);
		color: var(--color-text-muted);
	}
	.method-status.online {
		background: var(--color-success-tint);
		border: 1px solid var(--color-success-border);
		color: var(--color-success);
	}

	.method-desc {
		margin: 0;
		font-size: 11.5px;
		color: var(--color-text-secondary);
		line-height: 1.4;
	}

	.method-footer {
		display: flex;
		align-items: center;
		justify-content: space-between;
		gap: 8px;
		padding-top: 6px;
		border-top: 1px solid var(--color-border);
		flex-wrap: wrap;
	}

	.method-addr {
		font-size: 12px;
		color: var(--color-text-primary);
		min-width: 0;
		flex: 1;
		overflow-wrap: anywhere;
		word-break: break-word;
		line-height: 1.35;
	}

	.method-domains {
		display: flex;
		flex-direction: column;
		gap: 6px;
		padding-top: 6px;
		border-top: 1px solid var(--color-border);
	}

	.domain-row {
		display: flex;
		align-items: center;
		justify-content: space-between;
		gap: 10px;
		padding: 4px 0;
		border-bottom: 1px dashed var(--color-border);
	}
	.domain-row:last-child {
		border-bottom: none;
		padding-bottom: 0;
	}

	.domain-info {
		display: flex;
		flex-direction: column;
		align-items: flex-start;
		gap: 2px;
		min-width: 0;
		flex: 1;
	}

	.domain-host {
		font-size: 12.5px;
		font-weight: 600;
		color: var(--color-text-primary);
		overflow-wrap: anywhere;
		word-break: break-word;
		line-height: 1.3;
	}

	.domain-desc {
		font-size: 11px;
		color: var(--color-text-muted);
		overflow-wrap: anywhere;
		word-break: break-word;
		line-height: 1.3;
	}

	.font-mono {
		font-family: var(--font-mono);
	}
</style>
