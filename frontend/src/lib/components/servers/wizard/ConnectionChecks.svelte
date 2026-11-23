<script lang="ts">
	import { onMount, untrack } from 'svelte';
	import { api } from '$lib/api/client';
	import { onResourceInvalidated } from '$lib/stores/storeRegistry';
	import { Button } from '$lib/components/ui';
	import {
		lastServerVerification,
		recordServerVerification,
		pickFresherVerificationReport,
	} from '$lib/stores/serverVerification';
	import type {
		WizardKind,
		VerificationReport,
		VerificationErrorCategory,
	} from '$lib/types/serverWizard';
	import {
		CheckCircle2,
		AlertTriangle,
		XCircle,
		RefreshCw,
		ShieldCheck,
		Info,
	} from 'lucide-svelte';

	interface Props {
		kind?: WizardKind;
		currentFingerprint?: string;
		autoRun?: boolean;
		compact?: boolean;
	}

	let {
		kind = 'both',
		currentFingerprint = '',
		autoRun = false,
		compact = false,
	}: Props = $props();

	let loading = $state(false);
	let errorMsg = $state('');
	let report = $state<VerificationReport | null>(null);
	let checkedUIStateFingerprint = $state('');
	let liveBackendFingerprint = $state('');

	$effect(() => {
		if (autoRun) return;
		const shared =
			kind === 'xray'
				? $lastServerVerification.xray
				: kind === 'tgwebproxy'
					? $lastServerVerification.tgwebproxy
					: pickFresherVerificationReport(
							$lastServerVerification.xray,
							$lastServerVerification.tgwebproxy,
						);
		if (!shared) return;
		if (untrack(() => loading)) return;
		const cur = untrack(() => report);
		const next = pickFresherVerificationReport(shared, cur);
		if (next && next !== cur) {
			report = next;
			liveBackendFingerprint = next.state_fingerprint || '';
		}
	});

	let isStale = $derived.by(() => {
		if (!report) return false;
		if (
			currentFingerprint &&
			checkedUIStateFingerprint &&
			currentFingerprint !== checkedUIStateFingerprint
		) {
			return true;
		}
		if (
			report.state_fingerprint &&
			liveBackendFingerprint &&
			report.state_fingerprint !== liveBackendFingerprint
		) {
			return true;
		}
		return false;
	});

	const CATEGORY_LABELS: Record<VerificationErrorCategory, string> = {
		dns: 'DNS',
		tls: 'TLS / SNI',
		http_origin: 'Маршрут :9009 / служба',
		auth: 'Учётные записи',
		egress: 'Маршрут выхода',
		timeout: 'Таймаут',
	};

	function formatTime(iso: string): string {
		try {
			return new Date(iso).toLocaleTimeString('ru-RU', {
				hour: '2-digit',
				minute: '2-digit',
				second: '2-digit',
			});
		} catch {
			return iso;
		}
	}

	async function syncBackendFingerprint() {
		if (!report || loading) return;
		try {
			const caps = await api.getWizardCapabilities(kind);
			if (caps.state_fingerprint) {
				liveBackendFingerprint = caps.state_fingerprint;
			}
		} catch {
			// Ignore background poll error
		}
	}

	export async function runVerification() {
		loading = true;
		errorMsg = '';
		try {
			const res = await api.verifyWizard(kind);
			report = res;
			recordServerVerification(kind, res);
			checkedUIStateFingerprint = currentFingerprint;
			liveBackendFingerprint = res.state_fingerprint || '';
		} catch (e) {
			errorMsg = e instanceof Error ? e.message : 'Не удалось выполнить проверку подключения';
		} finally {
			loading = false;
		}
	}

	onMount(() => {
		if (autoRun) {
			void runVerification();
		}
		const handleFocus = () => {
			void syncBackendFingerprint();
		};
		if (typeof window !== 'undefined') {
			window.addEventListener('focus', handleFocus);
		}
		// Отпечаток состояния сервера меняется с правками ingress (VLESS,
		// Telegram, диспетчер) и с туннелями выхода — по их подсказкам и
		// перечитываем, таймер не нужен. Фокус окна остаётся страховкой.
		const unsubIngress = onResourceInvalidated('servers.ingress', () => void syncBackendFingerprint());
		const unsubTunnels = onResourceInvalidated('tunnels', () => void syncBackendFingerprint());

		return () => {
			if (typeof window !== 'undefined') {
				window.removeEventListener('focus', handleFocus);
			}
			unsubIngress();
			unsubTunnels();
		};
	});
</script>

<div class="checks-card" class:compact>
	<div class="checks-header">
		<div class="checks-title-wrap">
			<ShieldCheck size={16} class="icon-accent" />
			<span class="checks-title">Проверка готовности и маршрутов</span>
			{#if report}
				<span class="checked-time">Проверено в {formatTime(report.checked_at)}</span>
			{/if}
			{#if isStale}
				<span class="stale-badge">Конфигурация изменилась — проверьте повторно</span>
			{/if}
		</div>

		<Button
			variant="secondary"
			size="sm"
			onclick={runVerification}
			disabled={loading}
		>
			<RefreshCw size={13} class={loading ? 'spin mr-1' : 'mr-1'} />
			{loading ? 'Проверка...' : report ? 'Проверить снова' : 'Проверить подключение'}
		</Button>
	</div>

	{#if errorMsg}
		<div class="checks-error">
			<XCircle size={15} />
			<span>{errorMsg}</span>
		</div>
	{/if}

	{#if report}
		<div class="scope-banner">
			<Info size={14} class="scope-icon" />
			<span>{report.scope_note}</span>
		</div>

		<div class="checks-grid">
			{#each report.checks as chk (chk.id)}
				<div class="check-row status-{chk.status}" class:is-stale={isStale}>
					<div class="check-icon">
						{#if chk.status === 'ok'}
							<CheckCircle2 size={16} class="icon-ok" />
						{:else if chk.status === 'warning'}
							<AlertTriangle size={16} class="icon-warn" />
						{:else}
							<XCircle size={16} class="icon-err" />
						{/if}
					</div>
					<div class="check-body">
						<div class="check-top">
							<span class="check-name">{chk.title}</span>
							{#if chk.category}
								<span class="cat-badge">{CATEGORY_LABELS[chk.category] ?? chk.category}</span>
							{/if}
							{#if chk.target_address}
								<code class="target-code">{chk.target_address}</code>
							{/if}
						</div>
						<div class="check-msg">{chk.message}</div>
						{#if chk.remediation && chk.status !== 'ok'}
							<div class="check-remediation">Что проверить: {chk.remediation}</div>
						{/if}
					</div>
				</div>
			{/each}
		</div>
	{/if}
</div>

<style>
	.checks-card {
		background: var(--color-bg-primary);
		border: 1px solid var(--color-border);
		border-radius: var(--radius-sm);
		padding: 12px 14px;
		display: flex;
		flex-direction: column;
		gap: 10px;
	}
	.checks-card.compact {
		padding: 10px 12px;
	}
	.checks-header {
		display: flex;
		align-items: center;
		justify-content: space-between;
		gap: 10px;
		flex-wrap: wrap;
	}
	.checks-title-wrap {
		display: flex;
		align-items: center;
		gap: 8px;
		flex-wrap: wrap;
	}
	.checks-title {
		font-size: 13px;
		font-weight: 600;
		color: var(--color-text-primary);
	}
	.checked-time {
		font-size: 11px;
		color: var(--color-text-muted);
	}
	:global(.icon-accent) {
		color: var(--color-accent);
	}
	.stale-badge {
		font-size: 11px;
		padding: 2px 7px;
		border-radius: var(--radius-sm);
		background: var(--color-warning-tint);
		border: 1px solid var(--color-warning-border);
		color: var(--color-warning);
		font-weight: 500;
	}
	.scope-banner {
		display: flex;
		align-items: flex-start;
		gap: 8px;
		font-size: 11.5px;
		color: var(--color-text-muted);
		background: var(--color-bg-secondary);
		border: 1px solid var(--color-border);
		border-radius: var(--radius-sm);
		padding: 7px 10px;
		line-height: 1.4;
	}
	.checks-error {
		display: flex;
		align-items: center;
		gap: 8px;
		font-size: 12px;
		color: var(--color-error);
		background: var(--color-error-tint);
		border: 1px solid var(--color-error-border);
		padding: 8px 10px;
		border-radius: var(--radius-sm);
	}
	.checks-grid {
		display: flex;
		flex-direction: column;
		gap: 6px;
	}
	.check-row {
		display: flex;
		align-items: flex-start;
		gap: 10px;
		padding: 8px 10px;
		border-radius: var(--radius-sm);
		border: 1px solid var(--color-border);
		background: var(--color-bg-secondary);
	}
	.check-row.is-stale {
		opacity: 0.75;
	}
	.check-row.status-blocked {
		border-color: var(--color-error-border);
		background: var(--color-error-tint);
	}
	.check-row.status-warning {
		border-color: var(--color-warning-border);
		background: var(--color-warning-tint);
	}
	.check-icon {
		margin-top: 2px;
		flex-shrink: 0;
	}
	:global(.icon-ok) {
		color: var(--color-success);
	}
	:global(.icon-warn) {
		color: var(--color-warning);
	}
	:global(.icon-err) {
		color: var(--color-error);
	}
	.check-body {
		display: flex;
		flex-direction: column;
		gap: 3px;
		min-width: 0;
		flex: 1;
	}
	.check-top {
		display: flex;
		align-items: center;
		gap: 6px;
		flex-wrap: wrap;
	}
	.check-name {
		font-size: 12.5px;
		font-weight: 600;
		color: var(--color-text-primary);
		overflow-wrap: anywhere;
		word-break: break-word;
	}
	.cat-badge {
		font-size: 10px;
		padding: 1px 6px;
		border-radius: var(--radius-sm);
		background: var(--color-bg-primary);
		border: 1px solid var(--color-border);
		color: var(--color-text-muted);
	}
	.target-code {
		font-size: 11px;
		font-family: var(--font-mono);
		color: var(--color-text-muted);
		overflow-wrap: anywhere;
		word-break: break-word;
	}
	.check-msg {
		font-size: 12px;
		color: var(--color-text-secondary);
		overflow-wrap: anywhere;
		word-break: break-word;
	}
	.check-remediation {
		font-size: 11.5px;
		color: var(--color-text-muted);
		overflow-wrap: anywhere;
		word-break: break-word;
	}
	.spin {
		animation: spin 1s linear infinite;
	}
	@keyframes spin {
		from { transform: rotate(0deg); }
		to { transform: rotate(360deg); }
	}
</style>
