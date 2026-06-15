<!--
  Минимальная карта настроек движка FakeIP (MVP, 1E.7). Честный набор:
    - Движок: тумблер ON при routingMode==='fakeip-tun' && enabled. Сам API НЕ
      дёргает — вызывает onToggleEngine(turnOn), страница открывает ConfirmSwitch.
    - Перезапустить: кнопка → onRestart (страница зовёт api.singboxControl).
    - TCP/IP-стек: read-only «gvisor» (фиксирован для fakeip-tun).
    - WAN-интерфейс: read-only (wanAutoDetect ? 'Авто' : wanInterface).
    - Sniffing: read-only из settings.snifferEnabled (редактор отложен).
    - fakeip-пул / MTU: backend DefaultFakeIPTunParams, не в settings DTO —
      не выдумываем, показываем «по умолчанию».

  Полный дашборд (hero/live-traffic/active-selects) отложен (Slice 3+/2.2).
-->
<script lang="ts">
	import { Card, Toggle, Button } from '$lib/components/ui';
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
		if (restarting) return;
		restarting = true;
		try {
			await onRestart();
		} finally {
			restarting = false;
		}
	}
</script>

<Card padding="md">
	{#snippet header()}
		<h3 class="card-title">Движок</h3>
		<Button
			variant="secondary"
			size="sm"
			loading={restarting}
			disabled={restarting || !engineOn}
			onclick={handleRestart}
		>
			{#snippet iconBefore()}<RotateCw size={14} />{/snippet}
			Перезапустить
		</Button>
	{/snippet}

	<div class="settings">
		<!-- Движок toggle (controlled: страница решает, принять ли смену). -->
		<div class="row toggle-row">
			<Toggle
				checked={engineOn}
				controlled
				loading={toggleBusy}
				label="FakeIP-движок"
				hint="Включение/выключение проходит через подтверждение перехода."
				onchange={(next) => onToggleEngine(next)}
			/>
		</div>

		<!-- Read-only поля. -->
		<dl class="ro">
			<div class="ro-row">
				<dt>TCP/IP-стек</dt>
				<dd>gvisor</dd>
			</div>
			<div class="ro-row">
				<dt>WAN-интерфейс</dt>
				<dd>{wanLabel}</dd>
			</div>
			<div class="ro-row">
				<dt>Sniffing</dt>
				<!-- TODO(slice3): editable -->
				<dd>{snifferEnabled ? 'включён' : 'выключен'}</dd>
			</div>
			<div class="ro-row">
				<dt>fakeip-пул / MTU</dt>
				<dd class="muted">по умолчанию</dd>
			</div>
		</dl>
	</div>
</Card>

<style>
	.card-title {
		margin: 0;
		font-size: 0.9375rem;
		font-weight: 600;
		color: var(--text-primary);
	}

	.settings {
		display: flex;
		flex-direction: column;
		gap: 1rem;
	}

	.toggle-row {
		padding-bottom: 0.75rem;
		border-bottom: 1px solid var(--color-border);
	}

	.ro {
		margin: 0;
		display: flex;
		flex-direction: column;
		gap: 0.5rem;
	}

	.ro-row {
		display: grid;
		grid-template-columns: minmax(8rem, max-content) 1fr;
		gap: 0.75rem;
		font-size: 0.875rem;
	}

	.ro-row dt {
		color: var(--text-secondary);
	}

	.ro-row dd {
		margin: 0;
		color: var(--text-primary);
		font-weight: 500;
	}

	.ro-row dd.muted {
		color: var(--text-muted);
		font-weight: 400;
		font-style: italic;
	}
</style>
