<script lang="ts">
	import { Card } from '$lib/components/ui';
	import { Sparkles, Heart, Smile } from 'lucide-svelte';

	interface Props {
		serviceClass: string;
		glitter: number;
		onbuy: () => void;
	}

	let { serviceClass, glitter, onbuy }: Props = $props();
</script>

<Card padding="md">
	<div class="summary-card">
		<div class="summary-head">
			<Sparkles size={20} class="text-pink" />
			<h4>Итоговый расчет</h4>
		</div>

		<div class="summary-details">
			<div class="s-row">
				<span>Тариф экспресса:</span>
				<strong>{serviceClass.toUpperCase()}</strong>
			</div>
			<div class="s-row">
				<span>Стоимость в блестках:</span>
				<strong class="text-glitter">{glitter} ✨</strong>
			</div>
			<div class="s-row">
				<span>Спец. промокод:</span>
				<span class="promo-pill">KEENETIC-PONY (-100%)</span>
			</div>
			<div class="s-row total-row">
				<span>К оплате:</span>
				<span class="total-price">0 ₽ / 0 $</span>
			</div>
		</div>

		<div class="guarantee-box">
			<Smile size={16} />
			<span>100% гарантия хорошего настроения и отсутствия потери пакетов!</span>
		</div>

		<button type="button" class="btn-buy-pony" onclick={onbuy}>
			<Sparkles size={16} />
			<span>Получить билет в Страну Пони</span>
			<Heart size={16} />
		</button>
	</div>
</Card>

<style>
	.summary-card {
		display: flex;
		flex-direction: column;
		gap: 1rem;
		background: linear-gradient(180deg, rgba(236, 72, 153, 0.04) 0%, transparent 100%);
	}

	.summary-head {
		display: flex;
		align-items: center;
		gap: 0.4rem;
	}
	.summary-head h4 {
		margin: 0;
		font-size: 0.95rem;
		font-weight: 700;
		color: var(--color-text-primary);
	}

	.summary-details {
		display: flex;
		flex-direction: column;
		gap: 0.5rem;
		font-size: 0.84rem;
		padding: 0.6rem;
		background: var(--color-bg-secondary);
		border-radius: var(--radius-sm, 6px);
		border: 1px solid var(--color-border);
	}

	.s-row {
		display: flex;
		justify-content: space-between;
		align-items: center;
	}

	.total-row {
		border-top: 1px dashed var(--color-border);
		padding-top: 0.5rem;
		margin-top: 0.2rem;
		font-size: 0.95rem;
		font-weight: 800;
	}

	.total-price {
		color: #10b981;
		font-size: 1.1rem;
	}

	.text-glitter {
		color: #ec4899;
	}

	.promo-pill {
		font-size: 0.72rem;
		font-weight: 700;
		background: rgba(16, 185, 129, 0.15);
		color: #059669;
		padding: 0.1rem 0.4rem;
		border-radius: 4px;
	}

	.guarantee-box {
		display: flex;
		align-items: center;
		gap: 0.5rem;
		font-size: 0.76rem;
		color: var(--color-text-muted);
		padding: 0.5rem;
		background: var(--color-bg-secondary);
		border-radius: var(--radius-sm, 6px);
		border: 1px solid var(--color-border);
	}

	.btn-buy-pony {
		display: flex;
		align-items: center;
		justify-content: center;
		gap: 0.5rem;
		width: 100%;
		padding: 0.75rem;
		border-radius: var(--radius-sm, 8px);
		border: none;
		background: linear-gradient(135deg, #ec4899 0%, #d946ef 100%);
		color: #ffffff;
		font-weight: 700;
		font-size: 0.92rem;
		cursor: pointer;
		box-shadow: 0 4px 15px rgba(236, 72, 153, 0.35);
		transition: all 0.2s ease;
	}
	.btn-buy-pony:hover {
		transform: translateY(-2px);
		box-shadow: 0 6px 20px rgba(236, 72, 153, 0.5);
	}
</style>
