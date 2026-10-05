<script lang="ts">
	import { m } from '$lib/i18n';
	import { notifications } from '$lib/stores/notifications';
	import { poniesUnlocked } from '$lib/stores/poniesUnlocked';
	import { PonyHeroBanner, PonyBookingForm, PonyOrderSummary, PonyBoardingPass } from './ponies';
	import { ponyDestinations } from './ponies/labels';
	import type { PonyClass, PonyOption, TicketOrder } from './ponies/types';

	interface Props {
		onhide?: () => void;
	}

	let { onhide }: Props = $props();

	let passengerName = $state<string | null>(null);
	let destination = $state('ponyland');
	let serviceClass = $state<PonyClass>('vip');
	let optVpnImmunity = $state(true);
	let optMarshmallow = $state(true);
	let optRainbowBoost = $state(true);

	let purchasedTicket = $state<TicketOrder | null>(null);

	const destinations = $derived(ponyDestinations());

	function calculateGlitter(): number {
		let total = serviceClass === 'vip' ? 999 : serviceClass === 'business' ? 450 : 100;
		if (optVpnImmunity) total += 50;
		if (optMarshmallow) total += 20;
		if (optRainbowBoost) total += 99;
		return total;
	}

	function handleBuyTicket() {
		const randomNum = Math.floor(100000 + Math.random() * 900000);
		const randomRow = Math.floor(1 + Math.random() * 12);
		const randomLetter = ['A', 'B', 'C', 'PONY', 'VIP'][Math.floor(Math.random() * 5)];

		const selectedOpts: PonyOption[] = [];
		if (optVpnImmunity) selectedOpts.push('vpn');
		if (optMarshmallow) selectedOpts.push('marshmallow');
		if (optRainbowBoost) selectedOpts.push('rainbow');

		purchasedTicket = {
			passengerName: passengerName === null ? null : passengerName.trim(),
			destinationId: destination,
			serviceClass,
			options: selectedOpts,
			ticketNumber: `PONY-${randomNum}`,
			seat: `${randomRow}${randomLetter}`,
			priceGlitter: calculateGlitter(),
		};

		notifications.success(m.system_ponies_ticket_bought());
	}

	function resetOrder() {
		purchasedTicket = null;
	}

	function hideEasterEgg() {
		poniesUnlocked.lock();
		notifications.info(m.system_ponies_egg_hidden());
		if (onhide) onhide();
	}
</script>

<div class="pony-root">
	<PonyHeroBanner onhide={hideEasterEgg} />

	{#if !purchasedTicket}
		<div class="booking-grid">
			<PonyBookingForm
				{destinations}
				bind:passengerName
				bind:destination
				bind:serviceClass
				bind:optVpnImmunity
				bind:optMarshmallow
				bind:optRainbowBoost
			/>

			<PonyOrderSummary {serviceClass} glitter={calculateGlitter()} onbuy={handleBuyTicket} />
		</div>
	{:else}
		<PonyBoardingPass ticket={purchasedTicket} onreset={resetOrder} />
	{/if}
</div>

<style>
	.pony-root {
		display: flex;
		flex-direction: column;
		gap: 1rem;
		font-family: inherit;
	}

	.booking-grid {
		display: grid;
		grid-template-columns: 1.4fr 1fr;
		gap: 1rem;
	}

	@media (max-width: 850px) {
		.booking-grid {
			grid-template-columns: 1fr;
		}
	}
</style>
