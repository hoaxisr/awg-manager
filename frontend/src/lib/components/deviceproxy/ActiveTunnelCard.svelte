<script lang="ts">
	import { api } from '$lib/api/client';
	import { notifications } from '$lib/stores/notifications';
	import { Button } from '$lib/components/ui';
	import type { DeviceProxyOutbound, DeviceProxyRuntime } from '$lib/types';

	interface Props {
		outbounds: DeviceProxyOutbound[];
		runtime: DeviceProxyRuntime;
		onSwitched: () => void;
	}

	let { outbounds, runtime, onSwitched }: Props = $props();

	let switching = $state(false);
	let applying = $state(false);

	async function handleSelect(tag: string) {
		if (switching || !runtime.alive) return;
		const current = runtime.activeTag || runtime.defaultTag;
		if (tag === current) return;

		switching = true;
		try {
			await api.selectDeviceProxyRuntime(tag);
			notifications.success(`Активный туннель: ${labelFor(tag)}`);
			onSwitched();
		} catch (e) {
			notifications.error(`Не удалось переключить: ${(e as Error).message}`);
		} finally {
			switching = false;
		}
	}

	async function applyNow() {
		if (applying) return;
		applying = true;
		try {
			await api.applyDeviceProxy();
			notifications.success('Перезапуск выполнен, новая конфигурация активна');
			onSwitched();
		} catch (e) {
			notifications.error(`Не удалось применить: ${(e as Error).message}`);
		} finally {
			applying = false;
		}
	}

	function labelFor(tag: string): string {
		const ob = outbounds.find((o) => o.tag === tag);
		return ob?.label ?? tag;
	}

	const currentTag = $derived(runtime.alive ? (runtime.activeTag || runtime.defaultTag) : runtime.defaultTag);
	const isTemporary = $derived(runtime.alive && runtime.activeTag !== '' && runtime.activeTag !== runtime.defaultTag);
	const defaultLabel = $derived(labelFor(runtime.defaultTag));

	type Group = { title: string; items: DeviceProxyOutbound[] };

	const groups = $derived.by<Group[]>(() => {
		const direct = outbounds.filter((o) => o.kind === 'direct');
		const sb = outbounds.filter((o) => o.kind === 'singbox');
		const awg = outbounds.filter((o) => o.kind === 'awg');
		const out: Group[] = [];
		if (direct.length > 0) out.push({ title: '', items: direct });
		if (awg.length > 0) out.push({ title: 'Туннели', items: awg });
		if (sb.length > 0) out.push({ title: 'Sing-box туннели', items: sb });
		return out;
	});
</script>

<section class="card">
	<div class="card-header">
		<h2 class="section-title">Активный туннель</h2>
		<span class={runtime.alive ? 'badge badge-success' : 'badge badge-muted'}>
			{runtime.alive ? 'Работает сейчас' : 'Применится при запуске'}
		</span>
	</div>

	<div class="radio-list" class:disabled={switching || !runtime.alive}>
		{#each groups as group}
			{#if group.title}
				<div class="group-title">{group.title}</div>
			{/if}
			{#each group.items as ob}
				{@const checked = currentTag === ob.tag}
				<label class="option" class:checked>
					<input
						type="radio"
						name="device-proxy-active-tunnel"
						value={ob.tag}
						checked={checked}
						disabled={switching || !runtime.alive}
						onchange={() => handleSelect(ob.tag)}
					/>
					<span class="option-content">
						<span class="option-name">{ob.label || ob.tag}</span>
						{#if ob.detail || (ob.label && ob.label !== ob.tag)}
							<span class="option-meta">{ob.tag}{ob.detail ? ' · ' + ob.detail : ''}</span>
						{/if}
					</span>
					<span class="option-check" aria-hidden="true">
						{#if checked}
							<svg viewBox="0 0 24 24" width="16" height="16" fill="none" stroke="currentColor" stroke-width="3" stroke-linecap="round" stroke-linejoin="round">
								<polyline points="20 6 9 17 4 12"/>
							</svg>
						{/if}
					</span>
				</label>
			{/each}
		{/each}
	</div>

	{#if isTemporary}
		<div class="hint-row">
			<div class="hint-text">
				<span class="badge badge-warning">временно</span>
				После перезапуска вернётся к "{defaultLabel}"
			</div>
			<Button
				variant="ghost"
				size="sm"
				loading={applying}
				onclick={applyNow}
			>
				Применить сейчас
			</Button>
		</div>
	{:else if !runtime.alive}
		<div class="hint-row">
			<div class="hint-text">Запустите sing-box, чтобы переключать вживую.</div>
		</div>
	{/if}
</section>

<style>
	.section-title { font-size: 1rem; font-weight: 600; margin: 0; }

	.card-header {
		display: flex;
		align-items: center;
		justify-content: space-between;
		gap: 0.75rem;
		margin-bottom: 0.75rem;
	}

	.radio-list {
		display: flex;
		flex-direction: column;
		gap: 0.375rem;
		max-height: 360px;
		overflow-y: auto;
	}

	.radio-list.disabled {
		opacity: 0.6;
	}

	.group-title {
		font-size: 0.6875rem;
		text-transform: uppercase;
		letter-spacing: 0.05em;
		color: var(--color-text-muted);
		padding: 0.5rem 0 0.125rem;
	}

	.option {
		display: flex;
		align-items: center;
		gap: 0.75rem;
		padding: 0.625rem 0.875rem;
		background: var(--color-bg-secondary);
		border: 1px solid var(--color-border);
		border-radius: var(--radius);
		cursor: pointer;
		transition: background 0.15s ease, border-color 0.15s ease;
		min-width: 0;
	}

	.option:hover:not(.checked) {
		border-color: var(--color-border-hover);
		background: var(--color-bg-hover);
	}

	.option.checked {
		border-color: var(--color-accent);
		background: rgba(122, 162, 247, 0.08);
	}

	.option input[type='radio'] {
		position: absolute;
		opacity: 0;
		pointer-events: none;
		width: 0;
		height: 0;
	}

	.option-content {
		display: flex;
		flex-direction: column;
		gap: 0.125rem;
		flex: 1;
		min-width: 0;
	}

	.option-name {
		font-size: 0.875rem;
		color: var(--color-text-primary);
		font-weight: 500;
		overflow: hidden;
		text-overflow: ellipsis;
		white-space: nowrap;
	}

	.option-meta {
		font-family: var(--font-mono);
		font-size: 0.6875rem;
		color: var(--color-text-muted);
		overflow: hidden;
		text-overflow: ellipsis;
		white-space: nowrap;
	}

	.option-check {
		display: inline-flex;
		align-items: center;
		justify-content: center;
		width: 18px;
		height: 18px;
		flex-shrink: 0;
		color: var(--color-accent);
	}

	.hint-row {
		display: flex;
		align-items: center;
		justify-content: space-between;
		gap: 0.75rem;
		margin-top: 0.5rem;
		padding: 0.5rem 0.75rem;
		background: var(--color-bg-tertiary, var(--color-bg-secondary));
		border-radius: 6px;
	}

	.hint-text {
		display: flex;
		align-items: center;
		gap: 0.5rem;
		flex-wrap: wrap;
		font-size: 0.8125rem;
		color: var(--color-text-secondary);
	}

	.badge-muted {
		background: rgba(107, 114, 128, 0.15);
		color: var(--color-text-muted);
	}
</style>
