<script lang="ts">
	import { Card } from '$lib/components/ui';
	import { Compass, ShieldCheck, Coffee, Zap } from 'lucide-svelte';
	import type { PonyDestination } from './types';

	interface Props {
		destinations: PonyDestination[];
		passengerName: string;
		destination: string;
		serviceClass: string;
		optVpnImmunity: boolean;
		optMarshmallow: boolean;
		optRainbowBoost: boolean;
	}

	let {
		destinations,
		passengerName = $bindable(),
		destination = $bindable(),
		serviceClass = $bindable(),
		optVpnImmunity = $bindable(),
		optMarshmallow = $bindable(),
		optRainbowBoost = $bindable(),
	}: Props = $props();
</script>

<Card padding="md">
	<div class="form-container">
		<div class="section-title">
			<Compass size={18} class="text-pink" />
			<span>Параметры волшебного путешествия</span>
		</div>

		<label class="p-field">
			<span class="p-label">Имя пассажира:</span>
			<input type="text" bind:value={passengerName} placeholder="Введите ваше имя" />
		</label>

		<label class="p-field">
			<span class="p-label">Куда отправляемся:</span>
			<select bind:value={destination}>
				{#each destinations as d}
					<option value={d.name}>{d.name}</option>
				{/each}
			</select>
		</label>

		<!-- Class of service -->
		<div class="p-field">
			<span class="p-label">Класс обслуживания:</span>
			<div class="class-selector">
				<label class="class-card" class:active={serviceClass === 'vip'}>
					<input type="radio" bind:group={serviceClass} value="vip" />
					<div class="class-info">
						<div class="class-name">👑 VIP Пегас</div>
						<div class="class-sub">Личный пони-массажист и облачный шезлонг</div>
					</div>
					<span class="class-price">999 ✨</span>
				</label>

				<label class="class-card" class:active={serviceClass === 'business'}>
					<input type="radio" bind:group={serviceClass} value="business" />
					<div class="class-info">
						<div class="class-name">🎠 Зефирный Бизнес</div>
						<div class="class-sub">Карета с панорамным видом на радугу</div>
					</div>
					<span class="class-price">450 ✨</span>
				</label>

				<label class="class-card" class:active={serviceClass === 'eco'}>
					<input type="radio" bind:group={serviceClass} value="eco" />
					<div class="class-info">
						<div class="class-name">🦄 Эконом-Единорог</div>
						<div class="class-sub">Ветрено, зато весело и с ветерком</div>
					</div>
					<span class="class-price">100 ✨</span>
				</label>
			</div>
		</div>

		<!-- Extra options -->
		<div class="p-field">
			<span class="p-label">Волшебные доп. опции:</span>
			<div class="opts-list">
				<label class="opt-item">
					<input type="checkbox" bind:checked={optVpnImmunity} />
					<ShieldCheck size={16} class="text-pink" />
					<span>Иммунитет от блокировок Роскомнадзора в полете</span>
					<span class="opt-badge">+50 ✨</span>
				</label>
				<label class="opt-item">
					<input type="checkbox" bind:checked={optMarshmallow} />
					<Coffee size={16} class="text-pink" />
					<span>Бесконечный стакан какао с зефирками маршмеллоу</span>
					<span class="opt-badge">+20 ✨</span>
				</label>
				<label class="opt-item">
					<input type="checkbox" bind:checked={optRainbowBoost} />
					<Zap size={16} class="text-pink" />
					<span>Радужный ускоритель скорости (10 Гбит/с)</span>
					<span class="opt-badge">+99 ✨</span>
				</label>
			</div>
		</div>
	</div>
</Card>

<style>
	.form-container {
		display: flex;
		flex-direction: column;
		gap: 1rem;
	}

	.section-title {
		display: flex;
		align-items: center;
		gap: 0.45rem;
		font-weight: 700;
		font-size: 0.92rem;
		color: var(--color-text-primary);
	}
	.text-pink {
		color: #ec4899;
	}

	.p-field {
		display: flex;
		flex-direction: column;
		gap: 0.35rem;
	}

	.p-label {
		font-size: 0.78rem;
		font-weight: 600;
		color: var(--color-text-secondary);
	}

	.p-field input, .p-field select {
		padding: 0.45rem 0.65rem;
		border-radius: var(--radius-sm, 6px);
		border: 1px solid var(--color-border);
		background: var(--color-bg-secondary);
		color: var(--color-text-primary);
		font-size: 0.84rem;
	}
	.p-field input:focus, .p-field select:focus {
		border-color: #ec4899;
		outline: none;
		box-shadow: 0 0 0 2px rgba(236, 72, 153, 0.2);
	}

	/* Class Selector */
	.class-selector {
		display: flex;
		flex-direction: column;
		gap: 0.45rem;
	}

	.class-card {
		display: flex;
		align-items: center;
		justify-content: space-between;
		padding: 0.55rem 0.75rem;
		background: var(--color-bg-secondary);
		border: 1px solid var(--color-border);
		border-radius: var(--radius-sm, 6px);
		cursor: pointer;
		transition: all 0.15s ease;
		gap: 0.6rem;
	}
	.class-card:hover {
		background: var(--color-bg-tertiary);
	}
	.class-card.active {
		border-color: #ec4899;
		background: rgba(236, 72, 153, 0.08);
	}

	.class-info {
		flex: 1;
	}
	.class-name {
		font-size: 0.84rem;
		font-weight: 700;
		color: var(--color-text-primary);
	}
	.class-sub {
		font-size: 0.72rem;
		color: var(--color-text-muted);
	}

	.class-price {
		font-weight: 700;
		font-size: 0.82rem;
		color: #ec4899;
	}

	/* Options */
	.opts-list {
		display: flex;
		flex-direction: column;
		gap: 0.4rem;
	}

	.opt-item {
		display: flex;
		align-items: center;
		gap: 0.5rem;
		font-size: 0.8rem;
		color: var(--color-text-primary);
		cursor: pointer;
		padding: 0.35rem 0.5rem;
		border-radius: 4px;
		background: var(--color-bg-secondary);
		border: 1px solid var(--color-border);
	}
	.opt-badge {
		margin-left: auto;
		font-size: 0.72rem;
		font-weight: 600;
		color: #ec4899;
	}
</style>
