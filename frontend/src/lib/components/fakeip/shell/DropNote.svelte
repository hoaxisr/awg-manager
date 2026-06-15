<!--
  DropNote — честная заметка «что мы НЕ показываем» (мокап `.drop`). Перечисляет
  показатели, у которых НЕТ источника в sing-box/Clash API, чтобы не было соблазна
  их выдумать. Рендерится на «Обзоре» (сворачиваемый футер).

  Тон — warning (мокап красноватый «отсутствие данных»), без алармизма: это не
  ошибка, а сознательный отказ от фейка.
-->
<script lang="ts">
	import { ChevronDown, ChevronRight } from 'lucide-svelte';

	let open = $state(false);
</script>

<div class="drop" class:open>
	<button class="head" type="button" onclick={() => (open = !open)} aria-expanded={open}>
		{#if open}<ChevronDown size={14} />{:else}<ChevronRight size={14} />{/if}
		<span class="head-text">Что мы не показываем — и почему</span>
	</button>

	{#if open}
		<p class="body">
			Нет источника в sing-box/Clash API:
			<b>fakeip QPS</b>, <b>занятость пула</b>, <b>число cache-мэппингов</b>,
			<b>per-outbound throughput</b>, «hits 24h», «avg handshake».
			Egress-IP/ping и health outbound'ов — <b>только по запросу</b> (кнопкой),
			не live.
		</p>
	{/if}
</div>

<style>
	.drop {
		font-size: 0.6875rem;
		color: var(--text-secondary);
		background: var(--color-warning-tint);
		border: 1px solid var(--color-warning-border);
		border-radius: var(--radius, 8px);
	}

	.head {
		display: inline-flex;
		align-items: center;
		gap: 0.5rem;
		width: 100%;
		padding: 8px 12px;
		background: none;
		border: none;
		color: var(--text-secondary);
		font-size: inherit;
		font-family: inherit;
		cursor: pointer;
		text-align: left;
	}

	.head-text {
		font-weight: 600;
	}

	.body {
		margin: 0;
		padding: 0 12px 10px 32px;
		line-height: 1.6;
		color: var(--text-muted);
	}

	.body b {
		color: var(--color-warning);
		font-weight: 600;
	}
</style>
