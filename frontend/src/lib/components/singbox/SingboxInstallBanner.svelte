<script lang="ts">
	import { singbox } from '$lib/stores/singbox';
	import { api } from '$lib/api/client';

	const { status } = singbox;

	let installing = $state(false);
	let error = $state<string | null>(null);
	let dismissedKey = $state<string>('');

	const STORAGE_KEY = 'awgm:singbox-banner-dismissed';

	// Signature changes when install/proxyComponent/features state
	// changes, so a dismiss on one issue auto-resets once that issue is
	// resolved or replaced by a new one.
	let signature = $derived.by(() => {
		const s = $status;
		if (!s) return '';
		if (!s.installed) return 'not-installed';
		if (!s.proxyComponent) return 'no-proxy-component';
		// NaiveProxy requires the with_naive_outbound build tag. When
		// the installed binary lacks it, naive outbounds silently fail
		// at runtime — warn explicitly so the user swaps the build.
		if (s.features && s.features.length > 0 && !s.features.includes('with_naive_outbound')) {
			return 'no-naive';
		}
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
			await api.singboxInstall();
			await singbox.loadStatus();
		} catch (e) {
			error = e instanceof Error ? e.message : String(e);
		} finally {
			installing = false;
		}
	}
</script>

{#if visible && signature === 'not-installed'}
	<div class="banner">
		<div class="text">
			<strong>Sing-box не установлен</strong>
			<span>Установите для поддержки VLESS/Reality, Hysteria2, NaiveProxy</span>
		</div>
		<button class="btn btn-primary btn-sm" onclick={install} disabled={installing}>
			{installing ? 'Установка...' : 'Установить'}
		</button>
		<button class="dismiss" onclick={dismiss} title="Скрыть" aria-label="Скрыть">&times;</button>
		{#if error}
			<div class="error">{error}</div>
		{/if}
	</div>
{:else if visible && signature === 'no-proxy-component'}
	<div class="banner banner-error">
		<div class="text">
			<strong>NDMS-компонент «proxy» не установлен</strong>
			<span>
				Sing-box установлен, но без компонента <code>proxy</code> в Keenetic-прошивке
				интерфейсы Proxy0/1/… не создаются и трафик sing-box никуда не маршрутизируется.
				Добавьте компонент в веб-интерфейсе Keenetic (Настройки → Компоненты → «Клиент прокси»)
				и перезапустите этот демон.
			</span>
		</div>
		<button class="dismiss" onclick={dismiss} title="Скрыть" aria-label="Скрыть">&times;</button>
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
		<button class="dismiss" onclick={dismiss} title="Скрыть" aria-label="Скрыть">&times;</button>
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
	.text code {
		background: var(--bg-tertiary);
		padding: 0 4px;
		border-radius: 3px;
		font-family: ui-monospace, monospace;
		font-size: 0.8125rem;
	}
	.error { color: var(--error); font-size: 12px; }
	.dismiss {
		flex-shrink: 0;
		align-self: flex-start;
		background: transparent;
		border: none;
		color: var(--text-muted);
		font-size: 1.25rem;
		line-height: 1;
		padding: 2px 6px;
		border-radius: 4px;
		cursor: pointer;
		transition: color 0.15s, background 0.15s;
	}
	.dismiss:hover {
		color: var(--text-primary);
		background: var(--bg-hover);
	}
</style>
