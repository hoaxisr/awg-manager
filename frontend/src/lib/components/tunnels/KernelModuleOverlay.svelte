<script lang="ts">
	// Блокирующий оверлей «Модуль ядра недоступен». Выделен из
	// routes/+page.svelte при расщеплении навигации v3 (фаза 2): страниц с
	// туннелями стало две (/awg/tunnels и /tunnels), а оверлей — один.
	import { systemInfo } from '$lib/stores/system';
	import { TriangleAlert } from 'lucide-svelte';

	let sysInfo = $derived($systemInfo.data);
	let visible = $derived(
		sysInfo !== null &&
		!sysInfo.kernelModuleExists &&
		!sysInfo.kernelModuleLoaded &&
		!sysInfo.backendAvailability?.nativewg
	);
</script>

{#if visible}
	<div class="unsupported-overlay">
		<div class="unsupported-card">
			<div class="unsupported-icon">
				<TriangleAlert size={48} strokeWidth={1.5} aria-hidden="true" />
			</div>
			<h2 class="unsupported-title">Модуль ядра недоступен</h2>
			<p class="unsupported-text">
				Модель роутера <strong>{sysInfo?.kernelModuleModel || '(неизвестна)'}</strong> не имеет скомпилированный модуль ядра в настоящий момент.
			</p>
			<div class="unsupported-actions">
				<a href="https://t.me/awgmanager" target="_blank" rel="noopener" class="unsupported-link unsupported-link-primary">
					Написать в @awgmanager
				</a>
				<a href="https://gitlab.com/AmneziaVPN/amneziawg/amneziawg-linux-kernel-module" target="_blank" rel="noopener" class="unsupported-link">
					Установить вручную
				</a>
			</div>
		</div>
	</div>
{/if}

<style>
	.unsupported-overlay {
		position: fixed;
		inset: 0;
		z-index: var(--z-full-overlay);
		background: rgba(0, 0, 0, 0.85);
		display: flex;
		align-items: center;
		justify-content: center;
		padding: 1rem;
	}

	.unsupported-card {
		background: var(--color-bg-primary);
		border: 1px solid var(--color-border);
		border-radius: var(--radius);
		padding: 2rem;
		max-width: 420px;
		width: 100%;
		text-align: center;
		display: flex;
		flex-direction: column;
		align-items: center;
		gap: 1rem;
	}

	.unsupported-icon {
		color: var(--color-warning);
	}

	.unsupported-title {
		font-size: 1.25rem;
		font-weight: 600;
		margin: 0;
	}

	.unsupported-text {
		font-size: 0.875rem;
		color: var(--color-text-secondary);
		line-height: 1.6;
		margin: 0;
	}

	.unsupported-text strong {
		color: var(--color-text-primary);
	}

	.unsupported-actions {
		display: flex;
		flex-direction: column;
		gap: 0.5rem;
		width: 100%;
		margin-top: 0.5rem;
	}

	.unsupported-link {
		display: block;
		padding: 0.625rem 1rem;
		border-radius: var(--radius-sm);
		font-size: 0.875rem;
		font-weight: 500;
		text-decoration: none;
		text-align: center;
		transition: opacity var(--t-fast) ease;
		border: 1px solid var(--color-border);
		color: var(--color-text-secondary);
		background: var(--color-bg-secondary);
	}

	.unsupported-link:hover {
		opacity: 0.85;
	}

	.unsupported-link-primary {
		background: var(--color-accent);
		color: #fff;
		border-color: var(--color-accent);
	}
</style>
