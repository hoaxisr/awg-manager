<script lang="ts" module>
	export interface TopologyInbound {
		/** SH-07 / SH-08 / SH-09. */
		who: string;
		/** Порт входа: `DTLS :56002`, `Raw :56003`, `:56000`. */
		how: string;
	}
</script>

<script lang="ts">
	// Схема раздачи (SH-04..SH-17): кто подключается и куда попадает. Все
	// значения — из статуса и конфига; kernel-имён на схеме нет (ia §1.0), у
	// старого бинаря без `rawNdmsIface` строка Raw-половины не рисуется.
	import { ArrowRight } from 'lucide-svelte';

	interface Props {
		inbound: TopologyInbound[];
		/** Имя инстанса — узел роутера. */
		name: string;
		/** SH-10..SH-13 (WDTT) или адрес backend (FreeTurn). */
		routerLines: string[];
		/** SH-15 «политика: …»; пусто — строки нет. */
		policyLine?: string;
		/** SH-16 / SH-17; пусто — строки нет (у FreeTurn ingress не заводится). */
		ingressLine?: string;
	}

	let { inbound, name, routerLines, policyLine = '', ingressLine = '' }: Props = $props();
</script>

<div class="topo">
	<div class="col">
		<p class="col-title">Абоненты</p>
		{#each inbound as i (i.who)}
			<div class="node">
				<span class="node-name">{i.who}</span>
				<span class="node-sub">{i.how}</span>
			</div>
		{/each}
	</div>

	<div class="arrow"><ArrowRight size={18} strokeWidth={2.5} /></div>

	<div class="col">
		<p class="col-title">Этот роутер</p>
		<div class="node router">
			<span class="node-name">{name}</span>
			{#each routerLines as line (line)}
				<span class="node-sub">{line}</span>
			{/each}
		</div>
	</div>

	<div class="arrow"><ArrowRight size={18} strokeWidth={2.5} /></div>

	<div class="col">
		<p class="col-title">Выход</p>
		<div class="node">
			<span class="node-name">Интернет</span>
			{#if policyLine}<span class="node-sub">{policyLine}</span>{/if}
			{#if ingressLine}<span class="node-sub">{ingressLine}</span>{/if}
		</div>
	</div>
</div>

<style>
	.topo {
		display: flex;
		align-items: center;
		gap: 0.5rem;
		padding: 0.875rem;
		border: 1px solid var(--color-border);
		border-radius: var(--radius);
		background: var(--color-bg-tertiary);
		overflow-x: auto;
	}

	.col {
		display: flex;
		flex-direction: column;
		gap: 0.375rem;
		flex: 1 1 0;
		min-width: 150px;
	}

	.col-title {
		margin: 0;
		font-size: 0.6875rem;
		font-weight: 600;
		text-transform: uppercase;
		letter-spacing: 0.05em;
		color: var(--color-text-muted);
	}

	.node {
		display: flex;
		flex-direction: column;
		gap: 0.125rem;
		padding: 0.5rem 0.625rem;
		border: 1px solid var(--color-border);
		border-radius: var(--radius-sm);
		background: var(--color-bg-secondary);
	}

	.node.router {
		border-color: var(--color-accent);
	}

	.node-name {
		font-size: 0.8125rem;
		font-weight: 600;
		color: var(--color-text-primary);
	}

	.node-sub {
		font-size: 0.6875rem;
		color: var(--color-text-muted);
		font-family: var(--font-mono);
		word-break: break-all;
	}

	.arrow {
		display: flex;
		align-items: center;
		justify-content: center;
		color: var(--color-text-muted);
		flex: 0 0 auto;
	}

	/* Узкая ширина: колонки встают друг под друга, стрелки разворачиваются. */
	@media (max-width: 720px) {
		.topo {
			flex-direction: column;
			align-items: stretch;
		}

		.col {
			min-width: 0;
		}

		.arrow {
			transform: rotate(90deg);
		}
	}
</style>
