<!--
  Read-only зеркало inbound'ов merged-конфига sing-box (GET /api/singbox/inbounds),
  сгруппированное по источнику («Движок», «Прокси устройств», «Подписки»,
  «Сводные группы», «Туннели», «QoS», «Прочее»). Общий компонент для tproxy
  (ExpertPanel → SidePanel «Inbounds») и fakeip (InboundsTab) видов.

  Данные грузит РОДИТЕЛЬ (он же решает, когда refetch: mount / открытие панели)
  и передаёт entries + warnings; интерактивная часть device-proxy остаётся в
  родителях — сюда обычно передают список с исключённым source='deviceproxy'.
  Idle-записи («резерв порта») помечаются muted-бейджем с title-пояснением,
  почему inbound сохранён в конфиге (стабильность номеров портов).
-->
<script lang="ts">
	import { m } from '$lib/i18n';
	import { Badge } from '$lib/components/ui';
	import type { SingboxInboundEntry } from '$lib/types';
	import {
		groupInbounds,
		inboundListenLabel,
		idleBadgeLabel,
		idleTitle,
	} from '$lib/utils/singboxInbounds';

	interface Props {
		entries: SingboxInboundEntry[];
		warnings?: string[];
		/** Текст пустого состояния (когда entries пуст). */
		emptyText?: string;
		/** false — родитель рисует заголовок группы сам (список из одного источника). */
		showGroupHeaders?: boolean;
	}
	let {
		entries,
		warnings = [],
		emptyText,
		showGroupHeaders = true,
	}: Props = $props();

	const groups = $derived(groupInbounds(entries));
</script>

<div class="mirror">
	{#if warnings.length > 0}
		<p class="warn">{m.sb_router_inbounds_read_failed({ warnings: warnings.join('; ') })}</p>
	{/if}

	{#if groups.length === 0 && warnings.length === 0}
		<div class="empty">{emptyText ?? m.sb_router_inbounds_none()}</div>
	{:else}
		{#each groups as group (group.source)}
			<div class="group">
				{#if showGroupHeaders}
					<div class="group-head">
						<span class="group-title">{group.title}</span>
						<span class="group-count">{group.entries.length}</span>
					</div>
				{/if}
				<!-- Ключ slot:tag — коллизия тегов между слотами (рукой отредактированный
				     конфиг) не должна ронять зеркало (each_key_duplicate); о самой
				     коллизии сообщает warning бэкенда. -->
				{#each group.entries as e (`${e.slot}:${e.tag}`)}
					<div class="row" class:idle={e.idle}>
						<div class="row-main">
							<span class="ty">{e.type}</span>
							<span class="mono tag" title={e.tag}>{e.tag}</span>
							<span class="mono listen">{inboundListenLabel(e)}</span>
						</div>
						{#if e.ownerLabel || e.idle}
							<div class="row-sub">
								{#if e.ownerLabel}
									<span class="owner" title={e.ownerLabel}>{e.ownerLabel}</span>
								{/if}
								{#if e.idle}
									<span title={idleTitle(e)}>
										<Badge variant="muted" size="sm">{idleBadgeLabel(e)}</Badge>
									</span>
								{/if}
							</div>
						{/if}
					</div>
				{/each}
			</div>
		{/each}
	{/if}
</div>

<style>
	.mirror {
		display: flex;
		flex-direction: column;
		gap: 10px;
		min-width: 0;
	}
	.warn {
		margin: 0;
		font-size: 12px;
		color: var(--color-warning, #d97706);
	}
	.empty {
		padding: 14px;
		color: var(--text-muted);
		text-align: center;
		font-size: 12px;
	}
	.group {
		display: flex;
		flex-direction: column;
		min-width: 0;
	}
	.group-head {
		display: flex;
		align-items: center;
		gap: 6px;
		padding: 4px 0;
		font-size: 11px;
		letter-spacing: 0.06em;
		text-transform: uppercase;
		color: var(--text-muted);
	}
	.group-title {
		font-weight: 600;
	}
	.group-count {
		font-family: var(--font-mono);
	}
	.row {
		display: flex;
		flex-direction: column;
		gap: 2px;
		padding: 7px 0;
		border-bottom: 1px solid rgba(255, 255, 255, 0.04);
		min-width: 0;
	}
	.row:last-child {
		border-bottom: 0;
	}
	.row.idle {
		opacity: 0.75;
	}
	.row-main {
		display: flex;
		align-items: center;
		gap: 8px;
		min-width: 0;
	}
	.ty {
		font-size: 11px;
		color: var(--text-muted);
		font-family: var(--font-mono);
		flex-shrink: 0;
		border: 1px solid var(--border);
		border-radius: var(--radius-sm, 6px);
		padding: 0 5px;
	}
	.tag {
		font-size: 12px;
		color: var(--text-primary);
		overflow: hidden;
		text-overflow: ellipsis;
		white-space: nowrap;
		min-width: 0;
	}
	.listen {
		margin-left: auto;
		font-size: 12px;
		flex-shrink: 0;
	}
	.row-sub {
		display: flex;
		align-items: center;
		gap: 6px;
		min-width: 0;
		flex-wrap: wrap;
	}
	.owner {
		font-size: 12px;
		color: var(--text-muted);
		overflow: hidden;
		text-overflow: ellipsis;
		white-space: nowrap;
		min-width: 0;
	}
	.mono {
		font-family: var(--font-mono);
		color: var(--text-secondary);
	}
</style>
