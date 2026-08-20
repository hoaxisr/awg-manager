<script lang="ts">
	import { notifications } from '$lib/stores/notifications';
	import { poniesUnlocked } from '$lib/stores/poniesUnlocked';
	import { PonyHeroBanner, PonyBookingForm, PonyOrderSummary, PonyBoardingPass } from './ponies';
	import type { PonyDestination, TicketOrder } from './ponies/types';

	interface Props {
		onhide?: () => void;
	}

	let { onhide }: Props = $props();

	let passengerName = $state('Счастливый Пользователь');
	let destination = $state('Страна Розовых Пони (Облака из сахарной ваты)');
	let serviceClass = $state('vip');
	let optVpnImmunity = $state(true);
	let optMarshmallow = $state(true);
	let optRainbowBoost = $state(true);

	let purchasedTicket = $state<TicketOrder | null>(null);

	const destinations: PonyDestination[] = [
		{ id: 'ponyland', name: '🌸 Страна Розовых Пони (Облака из сахарной ваты)', desc: 'Пинг 0.00ms, реки из клубничного сиропа, пони встречают цветами' },
		{ id: 'friday', name: '🌴 Остров Вечной Пятницы (DPI выключен навсегда)', desc: 'Никаких замедлений YouTube и блокировок, вечный закат и чилл' },
		{ id: 'marshmallow', name: '☁ Зефирная Долина Свободного Интернета', desc: 'Связь через радужные мосты, скорость 100 Тбит/с прямо в космос' },
		{ id: 'nobugs', name: '🏰 Королевство Без Багов и Ребутов', desc: 'Роутер не греется, аптайм 999 лет, все скрипты работают с первого раза' },
	];

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

		const selectedOpts: string[] = [];
		if (optVpnImmunity) selectedOpts.push('Иммунитет от блокировок и DPI');
		if (optMarshmallow) selectedOpts.push('Горячее какао с зефирками');
		if (optRainbowBoost) selectedOpts.push('Радужный турбо-ускоритель 10G');

		purchasedTicket = {
			passengerName: passengerName.trim() || 'Любитель Пони',
			destination: destinations.find((d) => d.name === destination)?.name || destination,
			serviceClass: serviceClass === 'vip' ? '👑 VIP на Крылатом Пегасе' : serviceClass === 'business' ? '🎠 Бизнес в Зефирной Карете' : '🦄 Эконом на Единороге',
			options: selectedOpts,
			ticketNumber: `PONY-${randomNum}`,
			seat: `${randomRow}${randomLetter}`,
			flightTime: 'Через 5 минут от платформы 9¾',
			priceGlitter: calculateGlitter(),
		};

		notifications.success('✨ Билет оформлен! Пони готовы к вылету!');
	}

	function resetOrder() {
		purchasedTicket = null;
	}

	function hideEasterEgg() {
		poniesUnlocked.lock();
		notifications.info('Пасхалка снова спрятана');
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
