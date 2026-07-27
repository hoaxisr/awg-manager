<script lang="ts">
	import { onMount } from 'svelte';
	import { api } from '$lib/api/client';
	import type { DnsProxyInfo } from '$lib/types';
	import { RefreshCcw } from 'lucide-svelte';
	import { Button, Card } from '$lib/components/ui';
	import { EmptyState } from '$lib/components/layout';
	import {
		UpstreamsTable,
		PolicyStatRow,
		StaticRecordsCard,
		RebindCard,
	} from '$lib/components/diagnostics';
	import { notifications } from '$lib/stores/notifications';
	import { systemInfo } from '$lib/stores/system';
	import { copyToClipboard } from '$lib/utils/clipboard';
	import { downloadBlob } from '$lib/utils/download';
	import { dnsInfoReportFilename, formatDnsInfoReport } from '$lib/utils/dns-report';

	let info = $state<DnsProxyInfo | null>(null);
	let loading = $state(false);
	let errored = $state(false);

	// Upstreams/static/rebind are router-wide; show the first proxy's copy once.
	const shared = $derived(info?.proxies?.[0] ?? null);

	async function load() {
		loading = true;
		errored = false;
		try {
			info = await api.getDnsProxyInfo();
		} catch {
			errored = true;
		} finally {
			loading = false;
		}
	}

	async function getFreshRouterTimeOrWarn(): Promise<{ routerTime: string; routerOffset?: number } | null> {
		await systemInfo.refetch();
		const routerTime = $systemInfo.data?.routerTime;
		if (!routerTime) {
			notifications.warning('Время роутера ещё не загружено, попробуйте через несколько секунд');
			return null;
		}
		return {
			routerTime,
			routerOffset: $systemInfo.data?.routerTimezoneOffsetMinutes,
		};
	}

	async function copyData(): Promise<void> {
		if (!info || loading) return;
		const clock = await getFreshRouterTimeOrWarn();
		if (!clock) return;
		const { routerTime, routerOffset } = clock;
		const ok = await copyToClipboard(formatDnsInfoReport(info, routerTime, routerOffset));
		if (ok) notifications.success('DNS отчёт скопирован');
		else notifications.error('Не удалось скопировать DNS отчёт');
	}

	async function saveFile(): Promise<void> {
		if (!info || loading) return;
		const clock = await getFreshRouterTimeOrWarn();
		if (!clock) return;
		const { routerTime, routerOffset } = clock;
		const report = formatDnsInfoReport(info, routerTime, routerOffset);
		downloadBlob(
			new Blob([report], { type: 'text/plain;charset=utf-8' }),
			dnsInfoReportFilename(routerTime, routerOffset),
		);
	}

	onMount(load);
</script>

{#snippet refreshIcon()}
	<RefreshCcw size={14} strokeWidth={2} aria-hidden="true" />
{/snippet}

<div class="toolbar">
	<Button variant="secondary" size="sm" onclick={load} loading={loading} iconBefore={refreshIcon}>Обновить</Button>
	<Button variant="secondary" size="sm" onclick={copyData} disabled={!info || loading}>Скопировать данные</Button>
	<Button variant="secondary" size="sm" onclick={saveFile} disabled={!info || loading}>Сохранить файл</Button>
</div>

{#if loading && !info}
	<p class="hint">Загрузка сведений о DNS…</p>
{:else if errored}
	<p class="hint warn">Не удалось загрузить сведения о DNS.</p>
{:else if info && info.proxies.length > 0}
	<div class="dns-sections">
		{#if shared}
			<Card>
				<div class="card-label">Апстрим-серверы <span class="hint-inline">общие для роутера</span></div>
				<UpstreamsTable upstreams={shared.upstreams} />
			</Card>
		{/if}

		<Card>
			<div class="card-label">Статистика по политикам</div>
			{#each info.proxies as p, i}
				<PolicyStatRow proxy={p} open={i === 0} />
			{/each}
		</Card>

		{#if shared}
			<Card><StaticRecordsCard records={shared.staticRecords} /></Card>
			<Card><RebindCard rebind={shared.rebind} /></Card>
		{/if}
	</div>
{:else}
	<EmptyState title="Нет данных DNS-прокси" />
{/if}

<style>
	.toolbar {
		display: flex;
		flex-wrap: wrap;
		gap: 0.5rem;
		margin-bottom: 0.75rem;
	}
	.hint { font-size: 0.8125rem; color: var(--text-muted); margin: 0 0 0.75rem; }
	.warn { color: var(--warning); }
	.dns-sections { display: flex; flex-direction: column; gap: 16px; }
	.card-label { font-size: 11px; font-weight: 700; letter-spacing: .06em; text-transform: uppercase; color: var(--text-muted); margin-bottom: 12px; }
	.hint-inline { font-size: 11px; font-weight: 400; text-transform: none; letter-spacing: 0; margin-left: 8px; opacity: .8; }

	@media (max-width: 640px) {
		.toolbar {
			display: grid;
			grid-template-columns: repeat(2, minmax(0, 1fr));
			width: 100%;
		}

		.toolbar :global(.btn) {
			width: 100%;
			min-width: 0;
		}

		.toolbar :global(.btn:first-child .icon-before) {
			display: none;
		}

		.toolbar :global(.btn:last-child:nth-child(odd)) {
			grid-column: 1 / -1;
		}
	}
</style>
