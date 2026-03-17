<script lang="ts">
	import type { DiagMode } from '$lib/types';

	interface Props {
		onStart: (mode: DiagMode, restart: boolean) => void;
		disabled: boolean;
	}

	let { onStart, disabled }: Props = $props();
</script>

<div class="controls">
	<div class="buttons">
		<button
			class="btn btn-secondary"
			onclick={() => onStart('quick', false)}
			{disabled}
		>
			Быстрый тест
		</button>
		<button
			class="btn btn-secondary"
			onclick={() => onStart('quick', true)}
			{disabled}
		>
			Полная диагностика
		</button>
		<button
			class="btn btn-primary"
			onclick={() => onStart('full', true)}
			{disabled}
		>
			Сформировать отчёт
		</button>
	</div>

	<p class="controls-hint">
		Полная диагностика включает тест перезапуска туннелей. Соединение будет прервано на ~15 сек.
		Отчёт собирает полную информацию о системе, туннелях и маршрутах в JSON-файл.
	</p>
</div>

<style>
	.controls {
		display: flex;
		flex-direction: column;
		gap: 12px;
	}

	.buttons {
		display: flex;
		flex-wrap: wrap;
		gap: 8px;
	}

	.controls-hint {
		color: var(--text-tertiary);
		font-size: 13px;
		line-height: 1.4;
	}
</style>
