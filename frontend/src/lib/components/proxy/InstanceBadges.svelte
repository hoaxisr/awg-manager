<script lang="ts">
	// Бейджи шапки детали (ia.md §2.2 п.0, §3.2 п.0): режим подключения и
	// происхождение настроек. Оба — ТОЛЬКО индикаторы: режим переключается в
	// «Параметрах» (клиент) и в «Сети» (сервер), а происхождение неизменяемо.
	import { Badge, FieldHint } from '$lib/components/ui';
	import type { ProxyInstanceRow } from './rows';

	interface Props {
		row: ProxyInstanceRow;
		/**
		 * Режим из ЧЕРНОВИКА детали. Без него бейдж шёл по применённому
		 * (`row.mode`) и после переключения WG↔Raw оставался прежним до
		 * «Сохранить» — рядом с уже переключённым сегментом.
		 */
		mode?: 'wg' | 'raw';
	}

	let { row, mode }: Props = $props();

	// У FreeTurn режима нет вовсе — бейдж не рисуем, а не показываем «wg».
	const effectiveMode = $derived(mode ?? row.mode);
	const modeLabel = $derived(
		effectiveMode === 'raw' ? 'Raw' : effectiveMode === 'wg' ? 'WG' : '',
	);
	const modeHint = $derived(
		row.role === 'server'
			? 'WG — абоненты попадают в роутер через WireGuard-половину сервера. ' +
					'Raw — через raw-половину, без WireGuard. У режимов разные порты; ' +
					'смена применяется при перезапуске.'
			: 'WG — клиент поднимает WireGuard-туннель до сервера. Raw — работает без ' +
					'него, через raw-порт сервера. У режимов разные порты сервера и ' +
					'раздельно сохранённые адреса.',
	);
	const seededHint = $derived(
		`Настройки перенесены из ${row.seededFrom} при обновлении. Имя и параметры — ` +
			'те же, что были до него.',
	);
</script>

{#if modeLabel}
	<span class="badge-with-hint">
		<Badge size="sm" variant="default">{modeLabel}</Badge>
		<FieldHint text={modeHint} ariaLabel="Подсказка: режим подключения" />
	</span>
{/if}

{#if row.seededFrom}
	<span class="badge-with-hint">
		<Badge size="sm" variant="muted">перенесено</Badge>
		<FieldHint text={seededHint} ariaLabel="Подсказка: перенесённые настройки" />
	</span>
{/if}

<style>
	.badge-with-hint {
		display: inline-flex;
		align-items: center;
		gap: 0.25rem;
	}
</style>
