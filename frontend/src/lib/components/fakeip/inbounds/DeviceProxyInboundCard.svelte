<!--
  Карточка SOCKS/HTTP-входа (EDITABLE) по мокапу page-inbounds-v2 (.ic2).
  Один инстанс device-proxy: mixed-вход (SOCKS5 + HTTP) для устройств с
  ручным прокси. Правится через InboundSettingsDrawer (родитель), тумблер
  enabled персистится через saveDeviceProxyInstance (родитель).

  «протоколы» (SOCKS5 + HTTP) и «назначение» статичны: mixed-вход всегда
  принимает оба протокола, назначение фиксировано фичей device-proxy.
-->
<script lang="ts">
	import { Toggle } from '$lib/components/ui';
	import { Pencil, Trash2 } from 'lucide-svelte';

	interface Props {
		name: string;
		/** bind addr:port, e.g. «192.168.1.1:1080». */
		listen: string;
		authEnabled: boolean;
		enabled: boolean;
		/** runtime.alive — жив ли инстанс (для статус-точки). */
		alive: boolean;
		/** Движок запущен → точка достоверна, иначе muted. */
		live?: boolean;
		toggling?: boolean;
		onEdit: () => void;
		onToggle: (next: boolean) => void;
		onDelete: () => void;
	}
	let {
		name,
		listen,
		authEnabled,
		enabled,
		alive,
		live = false,
		toggling = false,
		onEdit,
		onToggle,
		onDelete,
	}: Props = $props();

	// Точка: зелёная только если инстанс включён, движок жив и runtime alive;
	// иначе muted (выключен / движок остановлен → не выдаём ложный «active»).
	const tone = $derived(enabled && live && alive ? 'success' : 'muted');
</script>

<article class="ic2">
	<div class="top">
		<span class="type">mixed</span>
		<span class="nm">{name}</span>
		<span class="dot" data-tone={tone} aria-hidden="true"></span>
	</div>

	<div class="rows">
		<div class="r"><span class="k">listen</span><span class="v y mono">{listen}</span></div>
		<div class="r"><span class="k">протоколы</span><span class="v">SOCKS5 + HTTP</span></div>
		<div class="r">
			<span class="k">авторизация</span>
			<span class="v dim">{authEnabled ? 'вкл' : 'выкл'}</span>
		</div>
		<div class="r">
			<span class="k">назначение</span>
			<span class="v dim">устройства с ручным прокси</span>
		</div>
	</div>

	<div class="foot">
		<button
			type="button"
			class="del"
			onclick={onDelete}
			aria-label={`Удалить inbound «${name}»`}
			title={`Удалить inbound «${name}»`}
		>
			<Trash2 size={14} aria-hidden="true" />
		</button>
		<div class="act">
			<button type="button" class="ib" onclick={onEdit}>
				<Pencil size={13} aria-hidden="true" /> изменить
			</button>
			<Toggle
				size="sm"
				controlled
				checked={enabled}
				loading={toggling}
				label={enabled ? 'выключить inbound' : 'включить inbound'}
				onchange={onToggle}
			/>
		</div>
	</div>
</article>

<style>
	.ic2 {
		background: var(--bg-secondary);
		border: 1px solid var(--border);
		border-radius: var(--radius, 12px);
		padding: 1rem;
		display: flex;
		flex-direction: column;
		min-width: 0;
	}

	.top {
		display: flex;
		align-items: center;
		gap: 0.5rem;
		margin-bottom: 0.625rem;
	}

	.type {
		font-size: 0.625rem;
		border-radius: 5px;
		padding: 0.1rem 0.4rem;
		border: 1px solid var(--border);
		color: var(--text-muted);
		font-family: var(--font-mono);
	}

	.nm {
		color: var(--text-primary);
		font-size: 0.9375rem;
		font-weight: 700;
		min-width: 0;
		overflow: hidden;
		text-overflow: ellipsis;
		white-space: nowrap;
	}

	.dot {
		width: 9px;
		height: 9px;
		border-radius: 50%;
		margin-left: auto;
		background: var(--text-muted);
		flex-shrink: 0;
	}

	.dot[data-tone='success'] {
		background: var(--color-success, #22c55e);
	}

	.rows {
		display: flex;
		flex-direction: column;
	}

	.r {
		display: flex;
		justify-content: space-between;
		align-items: baseline;
		gap: 0.75rem;
		padding: 0.45rem 0;
		border-top: 1px solid var(--border);
		font-size: 0.8125rem;
	}

	.r:first-child {
		border-top: none;
	}

	.k {
		color: var(--text-muted);
		flex-shrink: 0;
	}

	.v {
		color: var(--text-primary);
		text-align: right;
		min-width: 0;
		overflow-wrap: anywhere;
	}

	.v.y {
		color: var(--color-accent);
	}

	.v.dim {
		color: var(--text-secondary);
	}

	.v.mono {
		font-family: var(--font-mono);
	}

	.foot {
		display: flex;
		align-items: center;
		justify-content: space-between;
		gap: 0.5rem;
		margin-top: 0.75rem;
		border-top: 1px solid var(--border);
		padding-top: 0.7rem;
	}

	.act {
		display: inline-flex;
		align-items: center;
		gap: 0.5rem;
	}

	.ib {
		display: inline-flex;
		align-items: center;
		gap: 0.3rem;
		font-size: 0.6875rem;
		color: var(--text-secondary);
		background: none;
		border: 1px solid var(--border);
		border-radius: 6px;
		padding: 0.25rem 0.45rem;
		cursor: pointer;
	}

	.ib:hover {
		color: var(--text-primary);
		border-color: var(--text-muted);
	}

	.del {
		display: inline-flex;
		align-items: center;
		justify-content: center;
		color: var(--text-muted);
		background: none;
		border: none;
		padding: 0.2rem;
		cursor: pointer;
		border-radius: 6px;
	}

	.del:hover {
		color: var(--color-error);
		background: color-mix(in srgb, var(--color-error) 12%, transparent);
	}
</style>
