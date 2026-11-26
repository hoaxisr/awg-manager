<!--
  Пояснение вместо разделов FIRC, когда на роутере сторонний firc: панель им не
  управляет и его состояние не показывает. Если движок FIRC был выбран раньше —
  ещё и единственное доступное действие: уйти с него.
-->
<script lang="ts">
	import { ExternalLink, Info } from 'lucide-svelte';
	import { m } from '$lib/i18n';
	import { Button } from '$lib/components/ui';
	import { singboxStatus } from '$lib/stores/singbox';
	import { leaveFircEngine } from '$lib/utils/fircLeave';
	import { fircForeignWebUrl, type FircForeign } from '$lib/types/firc';

	interface Props {
		foreign: FircForeign;
		/** Движок FIRC всё ещё выбран: показываем конфликт и выход. */
		selected?: boolean;
		/** Без своей рамки — внутри карточки или окна. */
		bare?: boolean;
	}

	let { foreign, selected = false, bare = false }: Props = $props();

	const webUrl = $derived(fircForeignWebUrl(foreign, window.location));
	const singboxAbsent = $derived($singboxStatus.data?.installed === false);
	let leaving = $state(false);

	async function leaveEngine() {
		if (leaving) return;
		leaving = true;
		try {
			await leaveFircEngine();
		} finally {
			leaving = false;
		}
	}
</script>

<section class="foreign" class:bare aria-label={m.firc_foreign_title()}>
	<div class="head">
		<Info size={16} aria-hidden="true" />
		<h2 class="title">{m.firc_foreign_title()}</h2>
	</div>
	<p class="text">{m.firc_foreign_text()}</p>
	{#if foreign.package}
		<p class="found">{m.firc_foreign_package({ pkg: foreign.package })}</p>
	{:else if foreign.binary}
		<p class="found">{m.firc_foreign_binary({ path: foreign.binary })}</p>
	{/if}
	{#if selected}
		<p class="text conflict">{m.firc_foreign_conflict()}</p>
	{/if}
	{#if webUrl || selected}
		<div class="actions">
			{#if selected}
				<Button variant="primary" size="sm" onclick={leaveEngine} disabled={leaving}>
					{singboxAbsent ? m.firc_foreign_turn_off() : m.firc_foreign_leave()}
				</Button>
			{/if}
			{#if webUrl}
				<a class="weblink" href={webUrl} target="_blank" rel="noopener noreferrer">
					{m.firc_foreign_open_ui()}
					<ExternalLink size={14} aria-hidden="true" />
				</a>
			{/if}
		</div>
	{/if}
</section>

<style>
	.foreign {
		display: flex;
		flex-direction: column;
		gap: 0.5rem;
		padding: 1rem 1.125rem;
		border: 1px solid var(--border);
		border-radius: 8px;
		background: var(--bg-secondary);
		min-width: 0;
	}

	.foreign.bare {
		padding: 0;
		border: 0;
		background: none;
	}

	.head {
		display: flex;
		align-items: center;
		gap: 0.5rem;
		color: var(--text-secondary);
	}

	.title {
		margin: 0;
		font-size: var(--fs-base);
		font-weight: 600;
		color: var(--text-primary);
	}

	.text,
	.found {
		margin: 0;
		max-width: 70ch;
		font-size: var(--fs-md);
		line-height: 1.5;
		color: var(--text-secondary);
		overflow-wrap: anywhere;
	}

	.found {
		color: var(--text-muted);
	}

	.conflict {
		color: var(--text-primary);
	}

	.actions {
		display: flex;
		flex-wrap: wrap;
		align-items: center;
		gap: 0.5rem 1rem;
		margin-top: 0.25rem;
	}

	.weblink {
		display: inline-flex;
		align-items: center;
		gap: 0.375rem;
		font-size: var(--fs-md);
		color: var(--accent);
	}
</style>
