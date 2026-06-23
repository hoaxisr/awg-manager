<script lang="ts">
	import { browser } from '$app/environment';
	import { CircleAlert, X } from 'lucide-svelte';
	import { settings, usageLevel } from '$lib/stores/settings';

	const STORAGE_KEY_BASIC = 'awgm.welcomeBannerDismissed';
	const STORAGE_KEY_ADVANCED = 'awgm.welcomeBannerAdvancedDismissed';

	function readDismissed(key: string): boolean {
		return browser && localStorage.getItem(key) === '1';
	}

	let dismissedBasic = $state(readDismissed(STORAGE_KEY_BASIC));
	let dismissedAdvanced = $state(readDismissed(STORAGE_KEY_ADVANCED));

	const settingsReady = $derived($settings !== null);
	const visibleBasic = $derived(settingsReady && $usageLevel === 'basic' && !dismissedBasic);
	const visibleAdvanced = $derived(settingsReady && $usageLevel === 'advanced' && !dismissedAdvanced);

	function dismissBasic() {
		localStorage.setItem(STORAGE_KEY_BASIC, '1');
		dismissedBasic = true;
	}

	function dismissAdvanced() {
		localStorage.setItem(STORAGE_KEY_ADVANCED, '1');
		dismissedAdvanced = true;
	}
</script>

{#if visibleBasic}
	<div class="welcome-banner" role="status">
		<div class="banner-icon" aria-hidden="true">
			<CircleAlert size={20} />
		</div>
		<div class="banner-body">
			<strong>Вы в Базовом режиме</strong>
			<p>
				Доступны туннели, диагностика, NDMS и VPN для устройств. Чтобы открыть политики доступа,
				серверы, мониторинг, IP-маршруты и другие возможности — выберите более высокий уровень в
				<a href="/settings?mode">Настройках</a>.
			</p>
		</div>
		<button
			type="button"
			class="banner-close"
			aria-label="Скрыть подсказку"
			onclick={dismissBasic}
		>
			<X size={16} />
		</button>
	</div>
{/if}

{#if visibleAdvanced}
	<div class="welcome-banner" role="status">
		<div class="banner-icon" aria-hidden="true">
			<CircleAlert size={20} />
		</div>
		<div class="banner-body">
			<strong>Вы в Расширенном режиме</strong>
			<p>
				Если не хватает функционала — например: HR Neo или Sing-box Router — переключитесь на
				продвинутый режим в <a href="/settings?mode">настройках</a>.
				Если всё кажется слишком сложным, вернитесь на Базовый.
			</p>
		</div>
		<button
			type="button"
			class="banner-close"
			aria-label="Скрыть подсказку"
			onclick={dismissAdvanced}
		>
			<X size={16} />
		</button>
	</div>
{/if}

<style>
	.welcome-banner {
		display: flex;
		align-items: flex-start;
		gap: 0.75rem;
		padding: 0.875rem 1rem;
		margin-bottom: 1rem;
		background: var(--color-info-tint, var(--color-bg-tertiary));
		border: 1px solid var(--color-info, var(--color-border-strong));
		border-radius: var(--radius-md);
		color: var(--color-text-primary);
	}
	.banner-icon {
		flex-shrink: 0;
		color: var(--color-info, var(--color-accent));
	}
	.banner-body {
		flex: 1;
	}
	.banner-body strong {
		display: block;
		margin-bottom: 0.125rem;
	}
	.banner-body p {
		margin: 0;
		font-size: 0.875rem;
		color: var(--color-text-secondary);
	}
	.banner-body a {
		color: var(--color-accent);
		text-decoration: underline;
	}
	.banner-close {
		flex-shrink: 0;
		background: transparent;
		border: 0;
		color: var(--color-text-muted);
		cursor: pointer;
		padding: 0.25rem;
		border-radius: var(--radius-sm);
	}
	.banner-close:hover {
		color: var(--color-text-primary);
		background: var(--color-bg-hover);
	}
</style>
