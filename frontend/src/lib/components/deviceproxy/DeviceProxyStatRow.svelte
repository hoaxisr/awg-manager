<script lang="ts">
	import type { DeviceProxyConfig, DeviceProxyRuntime } from '$lib/types';

	interface Props {
		config: DeviceProxyConfig;
		runtime: DeviceProxyRuntime;
		bridgeLabel: string;
		activeLabel: string;
		onToggleEnabled: () => void;
		toggling: boolean;
	}

	let { config, runtime, bridgeLabel, activeLabel, onToggleEnabled, toggling }: Props = $props();

	type StateKind = 'alive' | 'waiting' | 'stopped';

	const stateKind = $derived<StateKind>(
		!config.enabled ? 'stopped' :
		runtime.alive ? 'alive' : 'waiting',
	);

	const stateText = $derived(
		stateKind === 'alive' ? 'ALIVE' :
		stateKind === 'waiting' ? 'WAITING' : 'STOPPED',
	);

	const listenText = $derived(
		config.listenAll ? 'Все интерфейсы' : (bridgeLabel || config.listenInterface),
	);

	const toggleHint = $derived(
		stateKind === 'alive' ? 'нажмите чтобы выключить' :
		stateKind === 'stopped' ? 'нажмите чтобы включить' :
		'',
	);
</script>

<div class="stat-row">
	<button
		type="button"
		class="tile tile-button"
		class:tile-success={stateKind === 'alive'}
		class:tile-warning={stateKind === 'waiting'}
		disabled={toggling || stateKind === 'waiting'}
		title={toggleHint}
		onclick={onToggleEnabled}
	>
		<div class="tile-label">Состояние{toggleHint ? ' · ' + toggleHint : ''}</div>
		<div class="tile-value">{toggling ? '...' : stateText}</div>
	</button>
	<div class="tile">
		<div class="tile-label">Слушает</div>
		<div class="tile-value">{listenText}</div>
	</div>
	<div class="tile">
		<div class="tile-label">Порт</div>
		<div class="tile-value">{config.port}</div>
	</div>
	<div class="tile">
		<div class="tile-label">Active</div>
		<div class="tile-value">{activeLabel || '—'}</div>
	</div>
</div>

<style>
	.stat-row {
		display: grid;
		grid-template-columns: repeat(4, minmax(0, 1fr));
		gap: 0.625rem;
		margin-bottom: 1rem;
	}

	.tile {
		padding: 0.625rem 0.875rem;
		background: var(--color-bg-secondary);
		border: 1px solid var(--color-border);
		border-radius: var(--radius);
	}

	.tile-button {
		font-family: inherit;
		text-align: left;
		cursor: pointer;
		transition: border-color 0.15s ease, background 0.15s ease;
	}

	.tile-button:hover:not(:disabled) {
		background: var(--color-bg-hover);
	}

	.tile-button:disabled {
		cursor: not-allowed;
		opacity: 0.7;
	}

	.tile-success {
		border-color: var(--color-success);
	}

	.tile-warning {
		border-color: var(--color-warning);
	}

	.tile-label {
		font-size: 0.6875rem;
		text-transform: uppercase;
		letter-spacing: 0.05em;
		color: var(--color-text-muted);
		margin-bottom: 0.25rem;
	}

	.tile-value {
		font-family: var(--font-mono);
		font-size: 0.9375rem;
		color: var(--color-text-primary);
		font-weight: 500;
		white-space: nowrap;
		overflow: hidden;
		text-overflow: ellipsis;
	}

	@media (max-width: 720px) {
		.stat-row {
			grid-template-columns: repeat(2, minmax(0, 1fr));
		}
	}
</style>
