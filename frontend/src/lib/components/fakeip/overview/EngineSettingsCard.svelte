<!--
  «Настройки движка» по мокапу dash3 (`.eform` — 2-колоночный грид строк
  ключ/значение). Честный набор:
    - Движок: «Перезапустить» (onRestart → api.singboxControl) + тумблер ON при
      routingMode==='fakeip-tun' && enabled. Сам API НЕ дёргает — onToggleEngine
      открывает ConfirmSwitch на странице.
    - TCP/IP-стек: read-only «gvisor» (фиксирован для fakeip-tun).
    - WAN-интерфейс: read-only (wanAutoDetect ? 'Авто' : wanInterface).
    - Sniffing (SNI/host): read-only из settings.snifferEnabled (редактор отложен).
    - fakeip-пул: backend DefaultFakeIPTunParams, не в settings DTO — не
      выдумываем, показываем «по умолчанию».
    - MTU tun: то же — «по умолчанию».

  Презентационный: значения/хендлеры приходят пропами.
-->
<script lang="ts">
	import { Toggle } from '$lib/components/ui';
	import { RotateCw } from 'lucide-svelte';

	interface Props {
		/** Движок включён в режиме fakeip-tun (routingMode==='fakeip-tun' && enabled). */
		engineOn: boolean;
		/** WAN: авто-детект интерфейса. */
		wanAutoDetect: boolean;
		/** WAN: явный системный интерфейс (когда не авто). */
		wanInterface?: string;
		/** Sniffing включён (read-only в этом MVP). */
		snifferEnabled: boolean;
		/** Запрос смены движка — открывает ConfirmSwitch на стороне страницы. */
		onToggleEngine: (turnOn: boolean) => void;
		/** Перезапуск sing-box — страница зовёт api.singboxControl('restart'). */
		onRestart: () => void | Promise<void>;
		/** Блокирует тумблер, пока переключение в полёте (управляется страницей). */
		toggleBusy?: boolean;
	}

	let {
		engineOn,
		wanAutoDetect,
		wanInterface,
		snifferEnabled,
		onToggleEngine,
		onRestart,
		toggleBusy = false,
	}: Props = $props();

	let restarting = $state(false);

	const wanLabel = $derived(wanAutoDetect ? 'Авто' : (wanInterface || '—'));

	async function handleRestart(): Promise<void> {
		if (restarting || !engineOn) return;
		restarting = true;
		try {
			await onRestart();
		} finally {
			restarting = false;
		}
	}
</script>

<div class="panel">
	<div class="ph">
		<span class="nm">Настройки движка</span>
		<span class="meta">применяются с перезапуском sing-box</span>
	</div>

	<div class="eform">
		<!-- Движок: перезапуск + тумблер (controlled — страница решает смену). -->
		<div class="erow">
			<span class="k">Движок</span>
			<span class="val">
				<button
					class="restart"
					type="button"
					onclick={handleRestart}
					disabled={restarting || !engineOn}
				>
					{#if restarting}
						<RotateCw size={12} class="spin" />
					{/if}
					Перезапустить
				</button>
				<Toggle
					checked={engineOn}
					controlled
					loading={toggleBusy}
					size="sm"
					onchange={(next) => onToggleEngine(next)}
				/>
			</span>
		</div>

		<!-- Read-only поля. -->
		<div class="erow">
			<span class="k">TCP/IP-стек</span>
			<span class="val"><span class="selbox">gvisor</span></span>
		</div>
		<div class="erow">
			<span class="k">WAN-интерфейс</span>
			<span class="val"><span class="selbox">{wanLabel}</span></span>
		</div>
		<div class="erow">
			<span class="k">Sniffing (SNI/host)</span>
			<!-- TODO(slice3): editable PUT snifferEnabled. -->
			<span class="val"><span class="selbox dim">{snifferEnabled ? 'включён' : 'выключен'}</span></span>
		</div>
		<div class="erow">
			<span class="k">fakeip-пул</span>
			<span class="val"><span class="selbox dim">по умолчанию</span></span>
		</div>
		<div class="erow">
			<span class="k">MTU tun</span>
			<span class="val"><span class="selbox dim">по умолчанию</span></span>
		</div>
	</div>
</div>

<style>
	.panel {
		background: var(--color-bg-secondary);
		border: 1px solid var(--color-border);
		border-radius: var(--radius, 12px);
		padding: 1rem;
	}

	.ph {
		display: flex;
		justify-content: space-between;
		align-items: center;
		margin-bottom: 0.625rem;
	}

	.ph .nm {
		color: var(--text-primary);
		font-size: 0.8125rem;
		font-weight: 700;
	}

	.ph .meta {
		color: var(--text-muted);
		font-size: 0.625rem;
	}

	/* 2-колоночный грид строк с тонкими разделителями (dash3 `.eform`). */
	.eform {
		display: grid;
		grid-template-columns: 1fr 1fr;
		gap: 1px;
		background: var(--color-border);
		border: 1px solid var(--color-border);
		border-radius: var(--radius-sm, 8px);
		overflow: hidden;
	}

	.erow {
		display: flex;
		align-items: center;
		justify-content: space-between;
		gap: 0.5rem;
		padding: 0.6875rem 0.875rem;
		background: var(--color-bg-secondary);
		font-size: 0.75rem;
	}

	.erow .k {
		color: var(--text-secondary);
	}

	.erow .val {
		display: flex;
		gap: 0.5rem;
		align-items: center;
	}

	.selbox {
		background: var(--color-bg-tertiary);
		border: 1px solid var(--color-border);
		border-radius: var(--radius-sm, 6px);
		padding: 0.3125rem 0.625rem;
		color: var(--color-accent);
		font-size: 0.75rem;
	}

	.selbox.dim {
		color: var(--text-muted);
	}

	.restart {
		color: var(--text-primary);
		background: none;
		border: 1px solid var(--color-border);
		border-radius: var(--radius-sm, 6px);
		padding: 0.25rem 0.5625rem;
		font-size: 0.6875rem;
		cursor: pointer;
		display: inline-flex;
		align-items: center;
		gap: 0.375rem;
	}

	.restart:hover:not(:disabled) {
		border-color: var(--color-border-hover);
	}

	.restart:disabled {
		opacity: 0.5;
		cursor: not-allowed;
	}

	.restart :global(.spin) {
		animation: spin 0.8s linear infinite;
	}

	@keyframes spin {
		to {
			transform: rotate(360deg);
		}
	}

	@media (max-width: 760px) {
		.eform {
			grid-template-columns: 1fr;
		}
	}
</style>
