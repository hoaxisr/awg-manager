<!--
  Подтверждение смены режима маршрутизации (FE-spec §7.2 + §12.4). Показывается
  ПЕРЕД переключением: текущий→новый режим, список «что произойдёт», амбер-блок
  последствий и действия Отмена/Переключить. Презентационный компонент — сам API
  не дёргает, действие принадлежит странице.
-->
<script lang="ts">
	import { Modal, Button } from '$lib/components/ui';
	import {
		humanLabel,
		switchConsequences,
		type RoutingMode,
	} from './switchConsequences';

	interface Props {
		open: boolean;
		from: RoutingMode;
		to: RoutingMode;
		onConfirm: () => void;
		onCancel: () => void;
		/** Число живых соединений; если неизвестно — цифра опускается, не выдумывается. */
		activeConnections?: number;
		/** Блокирует кнопку подтверждения, пока переключение в полёте. */
		busy?: boolean;
	}

	let {
		open,
		from,
		to,
		onConfirm,
		onCancel,
		activeConnections,
		busy = false,
	}: Props = $props();

	const title = $derived(
		`Переключение режима: ${humanLabel(from)} → ${humanLabel(to)}`,
	);
	const steps = $derived(switchConsequences(from, to));
	const enabling = $derived(to === 'fakeip-tun');

	// Цифра соединений показывается только когда известна (>= 0).
	const connSuffix = $derived(
		typeof activeConnections === 'number' ? ` (${activeConnections})` : '',
	);
</script>

<Modal {open} {title} size="md" onclose={onCancel} closeOnBackdrop={!busy}>
	<div class="confirm">
		<section class="block">
			<h4 class="block-title">Что произойдёт</h4>
			<ul class="steps">
				{#each steps as step}
					<li>{step}</li>
				{/each}
			</ul>
		</section>

		<section class="block amber">
			<ul class="warnings">
				<li>Активные соединения{connSuffix} будут разорваны и переустановлены.</li>
				<li>
					Полная доставка нового режима наступит после обновления DHCP-аренды у
					клиентов (до нескольких часов) или принудительного renew.
				</li>
				{#if enabling}
					<li>
						Устройства с собственным DoH/DoT резолвят мимо fakeip — их трафик не
						попадёт в туннель.
					</li>
					<li>
						Режим использует gvisor-стек: пропускная способность ниже TPROXY
						(ориентир ~25 Мбит/с на типовом SoC, меньше на слабых).
					</li>
				{/if}
			</ul>
		</section>
	</div>

	{#snippet actions()}
		<Button variant="secondary" size="md" onclick={onCancel}>Отмена</Button>
		<Button
			variant="primary"
			size="md"
			loading={busy}
			disabled={busy}
			onclick={onConfirm}
		>
			Переключить
		</Button>
	{/snippet}
</Modal>

<style>
	.confirm {
		display: flex;
		flex-direction: column;
		gap: 1rem;
	}

	.block-title {
		margin: 0 0 0.5rem;
		font-size: 0.875rem;
		font-weight: 600;
		color: var(--text-primary);
	}

	.steps,
	.warnings {
		margin: 0;
		padding-left: 1.25rem;
		display: flex;
		flex-direction: column;
		gap: 0.375rem;
		font-size: 0.875rem;
		line-height: 1.4;
	}

	.steps {
		color: var(--text-secondary);
	}

	.amber {
		padding: 0.75rem 0.875rem;
		border: 1px solid var(--color-warning-border);
		background: var(--color-warning-tint);
		border-radius: var(--radius);
	}

	.warnings {
		color: var(--color-warning);
	}
</style>
