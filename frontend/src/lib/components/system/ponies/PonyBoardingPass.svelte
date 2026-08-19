<script lang="ts">
	import { Button } from '$lib/components/ui';
	import { Printer, RotateCcw } from 'lucide-svelte';
	import type { TicketOrder } from './types';

	interface Props {
		ticket: TicketOrder;
		onreset: () => void;
	}

	let { ticket, onreset }: Props = $props();
</script>

<div class="ticket-view-wrap">
	<div class="boarding-pass">
		<div class="bp-left">
			<div class="bp-brand">
				<span class="bp-logo">🦄 PINK PONY AIRWAYS</span>
				<span class="bp-tag">BOARDING PASS / ПОСАДОЧНЫЙ ТАЛОН</span>
			</div>

			<div class="bp-grid">
				<div class="bp-item">
					<span class="bp-lbl">ПАССАЖИР / PASSENGER</span>
					<span class="bp-val">{ticket.passengerName}</span>
				</div>
				<div class="bp-item">
					<span class="bp-lbl">РЕЙС / FLIGHT</span>
					<span class="bp-val flight-val">{ticket.ticketNumber}</span>
				</div>
				<div class="bp-item full-w">
					<span class="bp-lbl">НАПРАВЛЕНИЕ / DESTINATION</span>
					<span class="bp-val dest-val">{ticket.destination}</span>
				</div>
				<div class="bp-item">
					<span class="bp-lbl">КЛАСС / CLASS</span>
					<span class="bp-val">{ticket.serviceClass}</span>
				</div>
				<div class="bp-item">
					<span class="bp-lbl">МЕСТО / SEAT</span>
					<span class="bp-val seat-val">{ticket.seat}</span>
				</div>
				<div class="bp-item full-w">
					<span class="bp-lbl">ВРЕМЯ ВЫЛЕТА / DEPARTURE</span>
					<span class="bp-val time-val">{ticket.flightTime}</span>
				</div>
				{#if ticket.options.length > 0}
					<div class="bp-item full-w">
						<span class="bp-lbl">ВКЛЮЧЕННЫЕ ЧУДЕСА:</span>
						<div class="bp-opts-tags">
							{#each ticket.options as opt}
								<span class="bp-tag-opt">🌸 {opt}</span>
							{/each}
						</div>
					</div>
				{/if}
			</div>
		</div>

		<div class="bp-divider">
			<div class="notch top"></div>
			<div class="dashed-line"></div>
			<div class="notch bottom"></div>
		</div>

		<div class="bp-right">
			<div class="bp-stub-top">
				<span class="emoji-pony">🦄✨</span>
				<div class="stub-flight">{ticket.ticketNumber}</div>
			</div>

			<div class="barcode-box">
				<div class="fake-barcode"></div>
				<span class="barcode-text">★ PINK-PONY-SPECIAL-PASS ★</span>
			</div>

			<div class="stub-footer">
				<span>МЕСТО: <strong>{ticket.seat}</strong></span>
				<span class="stamp-approved">ОДОБРЕНО РАДУГОЙ</span>
			</div>
		</div>
	</div>

	<div class="ticket-actions">
		<Button variant="secondary" onclick={() => window.print()}>
			{#snippet iconBefore()}<Printer size={14} />{/snippet}
			Распечатать билет
		</Button>
		<Button variant="primary" onclick={onreset}>
			{#snippet iconBefore()}<RotateCcw size={14} />{/snippet}
			Оформить еще один билет
		</Button>
	</div>
</div>

<style>
	.ticket-view-wrap {
		display: flex;
		flex-direction: column;
		align-items: center;
		gap: 1.25rem;
		padding: 1rem 0;
	}

	.boarding-pass {
		display: flex;
		width: 100%;
		max-width: 780px;
		background: #fff;
		border-radius: 12px;
		box-shadow: 0 10px 30px rgba(236, 72, 153, 0.25);
		border: 2px solid #f472b6;
		color: #1f2937;
		overflow: hidden;
		position: relative;
	}

	.bp-left {
		flex: 1;
		padding: 1.25rem;
		display: flex;
		flex-direction: column;
		gap: 1rem;
		background: linear-gradient(135deg, #fff5f7 0%, #ffffff 100%);
	}

	.bp-brand {
		display: flex;
		justify-content: space-between;
		align-items: center;
		border-bottom: 2px solid #fbcfe8;
		padding-bottom: 0.5rem;
	}
	.bp-logo {
		font-weight: 900;
		color: #db2777;
		font-size: 1rem;
		letter-spacing: 0.05em;
	}
	.bp-tag {
		font-size: 0.68rem;
		font-weight: 700;
		color: #9ca3af;
		letter-spacing: 0.05em;
	}

	.bp-grid {
		display: grid;
		grid-template-columns: 1fr 1fr;
		gap: 0.75rem 1rem;
	}

	.bp-item {
		display: flex;
		flex-direction: column;
		gap: 0.15rem;
	}
	.bp-item.full-w {
		grid-column: 1 / -1;
	}

	.bp-lbl {
		font-size: 0.65rem;
		font-weight: 700;
		color: #9ca3af;
		letter-spacing: 0.04em;
	}

	.bp-val {
		font-size: 0.88rem;
		font-weight: 700;
		color: #111827;
	}

	.flight-val {
		font-family: monospace;
		font-size: 1rem;
		color: #db2777;
	}

	.dest-val {
		color: #9333ea;
		font-size: 0.95rem;
	}

	.seat-val {
		font-size: 1.2rem;
		font-weight: 900;
		color: #db2777;
	}

	.time-val {
		color: #059669;
		font-weight: 700;
	}

	.bp-opts-tags {
		display: flex;
		flex-wrap: wrap;
		gap: 0.3rem;
		margin-top: 0.2rem;
	}
	.bp-tag-opt {
		font-size: 0.72rem;
		background: #fce7f3;
		color: #be185d;
		padding: 0.1rem 0.4rem;
		border-radius: 4px;
		font-weight: 600;
	}

	/* Divider */
	.bp-divider {
		position: relative;
		display: flex;
		flex-direction: column;
		justify-content: space-between;
		align-items: center;
		width: 1px;
		background: none;
	}
	.dashed-line {
		width: 1px;
		height: 100%;
		border-left: 2px dashed #f472b6;
	}
	.notch {
		position: absolute;
		width: 20px;
		height: 20px;
		background: var(--color-bg-primary, #0f172a);
		border-radius: 50%;
		z-index: 2;
	}
	.notch.top {
		top: -10px;
		left: -10px;
	}
	.notch.bottom {
		bottom: -10px;
		left: -10px;
	}

	/* Stub Right */
	.bp-right {
		width: 190px;
		padding: 1.25rem 1rem;
		display: flex;
		flex-direction: column;
		justify-content: space-between;
		align-items: center;
		text-align: center;
		background: #fdf2f8;
	}

	.emoji-pony {
		font-size: 2rem;
	}
	.stub-flight {
		font-family: monospace;
		font-weight: 800;
		color: #db2777;
		font-size: 0.9rem;
	}

	.fake-barcode {
		width: 120px;
		height: 45px;
		background: repeating-linear-gradient(
			90deg,
			#111827,
			#111827 2px,
			transparent 2px,
			transparent 4px,
			#111827 4px,
			#111827 7px,
			transparent 7px,
			transparent 9px
		);
		margin: 0.5rem auto;
	}
	.barcode-text {
		font-size: 0.6rem;
		color: #6b7280;
		font-family: monospace;
	}

	.stub-footer {
		display: flex;
		flex-direction: column;
		gap: 0.3rem;
		font-size: 0.75rem;
	}

	.stamp-approved {
		display: inline-block;
		border: 2px solid #ec4899;
		color: #ec4899;
		font-size: 0.62rem;
		font-weight: 900;
		padding: 0.1rem 0.3rem;
		border-radius: 4px;
		transform: rotate(-5deg);
	}

	.ticket-actions {
		display: flex;
		gap: 0.75rem;
	}

	@media (max-width: 850px) {
		.boarding-pass {
			flex-direction: column;
		}
		.bp-right {
			width: 100%;
			border-top: 2px dashed #f472b6;
		}
		.bp-divider {
			display: none;
		}
	}
</style>
