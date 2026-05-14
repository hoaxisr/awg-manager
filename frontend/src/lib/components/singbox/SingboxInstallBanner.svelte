<script lang="ts">
	import { singboxStatus } from '$lib/stores/singbox';
	import { singboxInstallProgress } from '$lib/stores/singboxInstall';
	import { api } from '$lib/api/client';
	import { Button, IconButton } from '$lib/components/ui';
	import { formatBytes } from '$lib/utils/format';

	let installing = $state(false);
	let updating = $state(false);
	let error = $state<string | null>(null);
	let dismissedKey = $state<string>('');

	const progress = $derived($singboxInstallProgress);
	const phaseLabel = $derived.by(() => {
		const p = progress;
		if (!p) return '';
		switch (p.phase) {
			case 'download':
				if (p.total > 0) {
					const pct = Math.min(100, Math.round((p.downloaded / p.total) * 100));
					return `Скачивание ${pct}% (${formatBytes(p.downloaded)} / ${formatBytes(p.total)})`;
				}
				return `Скачивание (${formatBytes(p.downloaded)})`;
			case 'activate':
				return 'Установка…';
			case 'stop':
				return 'Остановка sing-box…';
			case 'start':
				return 'Запуск sing-box…';
			case 'done':
				return 'Готово';
			case 'error':
				return p.error ? `Ошибка: ${p.error}` : 'Ошибка';
			default:
				return '';
		}
	});
	const progressPct = $derived.by(() => {
		const p = progress;
		if (!p || p.phase !== 'download' || p.total <= 0) return null;
		return Math.min(100, Math.round((p.downloaded / p.total) * 100));
	});

	const STORAGE_KEY = 'awgm:singbox-banner-dismissed';

	// Signature changes when install/proxyComponent/features state
	// changes, so a dismiss on one issue auto-resets once that issue is
	// resolved or replaced by a new one.
	let signature = $derived.by(() => {
		const s = $singboxStatus.data;
		if (!s) return '';
		if (!s.installed) return 'not-installed';
		if (!s.proxyComponent) return 'no-proxy-component';
		// NaiveProxy requires the with_naive_outbound build tag. When
		// the installed binary lacks it, naive outbounds silently fail
		// at runtime — warn explicitly so the user swaps the build.
		if (s.features && s.features.length > 0 && !s.features.includes('with_naive_outbound')) {
			return 'no-naive';
		}
		if (s.updateAvailable) return 'update-available';
		return '';
	});

	$effect(() => {
		if (typeof window === 'undefined') return;
		dismissedKey = window.localStorage.getItem(STORAGE_KEY) ?? '';
	});

	let visible = $derived(signature !== '' && dismissedKey !== signature);

	function dismiss(): void {
		if (typeof window === 'undefined') return;
		window.localStorage.setItem(STORAGE_KEY, signature);
		dismissedKey = signature;
	}

	async function install(): Promise<void> {
		installing = true;
		error = null;
		try {
			const fresh = await api.singboxInstall();
			singboxStatus.applyMutationResponse(fresh);
		} catch (e) {
			error = e instanceof Error ? e.message : String(e);
		} finally {
			installing = false;
		}
	}

	async function update(): Promise<void> {
		updating = true;
		error = null;
		try {
			const fresh = await api.singboxUpdate();
			singboxStatus.applyMutationResponse(fresh);
		} catch (e) {
			error = e instanceof Error ? e.message : String(e);
		} finally {
			updating = false;
		}
	}
</script>

{#if visible && signature === 'not-installed'}
	<div class="banner banner-stack">
		<div class="banner-row">
			<div class="text">
				<strong>Sing-box не установлен</strong>
				<span>Установите для поддержки VLESS/Reality, Hysteria2, NaiveProxy</span>
				<span class="hint">Установка sing-box требует большого количества свободного пространства. Необходимо использовать Entware на внешнем носителе.</span>
			</div>
			{#if !progress}
				<Button variant="primary" size="sm" onclick={install} loading={installing}>
					{installing ? 'Установка...' : 'Установить'}
				</Button>
			{/if}
			<IconButton ariaLabel="Скрыть" onclick={dismiss}>&times;</IconButton>
		</div>
		{#if progress}
			<div class="progress-widget" class:progress-error={progress.phase === 'error'} class:progress-done={progress.phase === 'done'}>
				<div class="progress-label">{phaseLabel}</div>
				<div class="progress-bar" class:indeterminate={progressPct === null && progress.phase !== 'done' && progress.phase !== 'error'}>
					<div
						class="progress-fill"
						style:width={progressPct !== null ? `${progressPct}%` : '100%'}
					></div>
				</div>
			</div>
		{/if}
		{#if error}
			<div class="error">{error}</div>
		{/if}
	</div>
{:else if visible && signature === 'no-proxy-component'}
	<div class="banner banner-error">
		<div class="text">
			<strong>NDMS-компонент «proxy» не установлен</strong>
			<span>
				Sing-box установлен, но без компонента <code>proxy</code> в прошивке роутера
				интерфейсы Proxy0/1/… не создаются и трафик sing-box никуда не маршрутизируется.
				Добавьте компонент в веб-интерфейсе роутера (Настройки → Компоненты → «Клиент прокси»)
				и перезапустите этот демон.
			</span>
		</div>
		<IconButton ariaLabel="Скрыть" onclick={dismiss}>&times;</IconButton>
	</div>
{:else if visible && signature === 'no-naive'}
	<div class="banner">
		<div class="text">
			<strong>Sing-box собран без поддержки NaiveProxy</strong>
			<span>
				В установленной сборке отсутствует тег <code>with_naive_outbound</code>.
				VLESS/Reality и Hysteria2 работают, но NaiveProxy-туннели при запуске будут
				отвергнуты сингбоксом. Установите сборку с этим тегом, если нужен NaiveProxy.
			</span>
		</div>
		<IconButton ariaLabel="Скрыть" onclick={dismiss}>&times;</IconButton>
	</div>
{:else if visible && signature === 'update-available'}
	<div class="banner banner-stack">
		<div class="banner-row">
			<div class="text">
				<strong>Доступна новая версия sing-box</strong>
				<span>
					Текущая <code>{$singboxStatus.data?.currentVersion ?? '—'}</code> →
					<code>{$singboxStatus.data?.requiredVersion}</code>
				</span>
			</div>
			{#if !progress}
				<Button variant="primary" size="sm" onclick={update} loading={updating} disabled={updating}>
					{updating ? 'Обновление...' : 'Обновить sing-box'}
				</Button>
			{/if}
			<IconButton ariaLabel="Скрыть" onclick={dismiss}>&times;</IconButton>
		</div>
		{#if progress}
			<div class="progress-widget" class:progress-error={progress.phase === 'error'} class:progress-done={progress.phase === 'done'}>
				<div class="progress-label">{phaseLabel}</div>
				<div class="progress-bar" class:indeterminate={progressPct === null && progress.phase !== 'done' && progress.phase !== 'error'}>
					<div
						class="progress-fill"
						style:width={progressPct !== null ? `${progressPct}%` : '100%'}
					></div>
				</div>
			</div>
		{/if}
		{#if error}
			<div class="error">{error}</div>
		{/if}
	</div>
{/if}

<style>
	.banner {
		display: flex;
		align-items: center;
		gap: 1rem;
		padding: 1rem;
		border: 1px solid var(--warning);
		background: rgba(245, 158, 11, 0.08);
		border-radius: var(--radius);
		margin-bottom: 1rem;
	}
	.banner-error {
		border-color: var(--error);
		background: rgba(239, 68, 68, 0.08);
	}
	.text { flex: 1; display: flex; flex-direction: column; gap: 4px; }
	.text .hint {
		font-size: 0.75rem;
		color: var(--text-muted);
		margin-top: 2px;
	}
	.text code {
		background: var(--bg-tertiary);
		padding: 0 4px;
		border-radius: 3px;
		font-family: ui-monospace, monospace;
		font-size: 0.8125rem;
	}
	.error { color: var(--error); font-size: 12px; }

	.banner-stack {
		flex-direction: column;
		align-items: stretch;
		gap: 0.75rem;
	}
	.banner-row {
		display: flex;
		align-items: center;
		gap: 1rem;
	}
	.progress-widget {
		display: flex;
		flex-direction: column;
		gap: 0.35rem;
	}
	.progress-label {
		font-size: 0.78rem;
		color: var(--color-text-primary, var(--text-primary));
		font-variant-numeric: tabular-nums;
	}
	.progress-bar {
		position: relative;
		height: 6px;
		background: var(--bg-tertiary, rgba(0, 0, 0, 0.08));
		border-radius: 3px;
		overflow: hidden;
	}
	.progress-fill {
		position: absolute;
		left: 0;
		top: 0;
		bottom: 0;
		background: var(--color-primary, #3b82f6);
		transition: width 120ms ease-out;
	}
	.progress-bar.indeterminate .progress-fill {
		background: linear-gradient(
			90deg,
			transparent 0%,
			var(--color-primary, #3b82f6) 50%,
			transparent 100%
		);
		background-size: 200% 100%;
		animation: indeterminate-slide 1.2s linear infinite;
		width: 100% !important;
	}
	.progress-widget.progress-error .progress-fill {
		background: var(--error, #ef4444);
	}
	.progress-widget.progress-done .progress-fill {
		background: var(--success, #10b981);
	}
	@keyframes indeterminate-slide {
		0% { background-position: 200% 0; }
		100% { background-position: -100% 0; }
	}
</style>
