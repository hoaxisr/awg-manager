<script lang="ts">
	import { Button, Card } from '$lib/components/ui';
	import { notifications } from '$lib/stores/notifications';
	import { poniesUnlocked } from '$lib/stores/poniesUnlocked';
	import {
		Sparkles,
		Heart,
		Compass,
		ShieldCheck,
		Coffee,
		Zap,
		Smile,
		Printer,
		RotateCcw,
		EyeOff,
	} from 'lucide-svelte';

	interface Props {
		onhide?: () => void;
	}

	let { onhide }: Props = $props();

	interface TicketOrder {
		passengerName: string;
		destination: string;
		serviceClass: string;
		options: string[];
		ticketNumber: string;
		seat: string;
		flightTime: string;
		priceGlitter: number;
	}

	let passengerName = $state('Счастливый Пользователь');
	let destination = $state('Страна Розовых Пони (Облака из сахарной ваты)');
	let serviceClass = $state('vip');
	let optVpnImmunity = $state(true);
	let optMarshmallow = $state(true);
	let optRainbowBoost = $state(true);

	let purchasedTicket = $state<TicketOrder | null>(null);

	const destinations = [
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
	<!-- Top Banner -->
	<div class="pony-hero-banner">
		<div class="hero-content">
			<div class="pony-icon-wrap">
				<span class="emoji-huge">🦄</span>
			</div>
			<div class="hero-texts">
				<div class="hero-title-row">
					<h2>Касса межгалактических экспрессов «Розовый Пони»</h2>
					<button type="button" class="btn-hide-egg" title="Спрятать эту вкладку обратно" onclick={hideEasterEgg}>
						<EyeOff size={13} />
						<span>Спрятать пасхалку</span>
					</button>
				</div>
				<p>Секретный раздел для тех, кто устал от конфигураций и туннелей. Здесь интернет свободен, а роутеры никогда не зависают!</p>
			</div>
		</div>
	</div>

	{#if !purchasedTicket}
		<!-- Booking Form -->
		<div class="booking-grid">
			<!-- Form Column -->
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

			<!-- Summary / Checkout Card -->
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
							<strong class="text-glitter">{calculateGlitter()} ✨</strong>
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

					<button type="button" class="btn-buy-pony" onclick={handleBuyTicket}>
						<Sparkles size={16} />
						<span>Получить билет в Страну Пони</span>
						<Heart size={16} />
					</button>
				</div>
			</Card>
		</div>
	{:else}
		<!-- Boarding Pass / Purchased Ticket -->
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
							<span class="bp-val">{purchasedTicket.passengerName}</span>
						</div>
						<div class="bp-item">
							<span class="bp-lbl">РЕЙС / FLIGHT</span>
							<span class="bp-val flight-val">{purchasedTicket.ticketNumber}</span>
						</div>
						<div class="bp-item full-w">
							<span class="bp-lbl">НАПРАВЛЕНИЕ / DESTINATION</span>
							<span class="bp-val dest-val">{purchasedTicket.destination}</span>
						</div>
						<div class="bp-item">
							<span class="bp-lbl">КЛАСС / CLASS</span>
							<span class="bp-val">{purchasedTicket.serviceClass}</span>
						</div>
						<div class="bp-item">
							<span class="bp-lbl">МЕСТО / SEAT</span>
							<span class="bp-val seat-val">{purchasedTicket.seat}</span>
						</div>
						<div class="bp-item full-w">
							<span class="bp-lbl">ВРЕМЯ ВЫЛЕТА / DEPARTURE</span>
							<span class="bp-val time-val">{purchasedTicket.flightTime}</span>
						</div>
						{#if purchasedTicket.options.length > 0}
							<div class="bp-item full-w">
								<span class="bp-lbl">ВКЛЮЧЕННЫЕ ЧУДЕСА:</span>
								<div class="bp-opts-tags">
									{#each purchasedTicket.options as opt}
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
						<div class="stub-flight">{purchasedTicket.ticketNumber}</div>
					</div>

					<div class="barcode-box">
						<div class="fake-barcode"></div>
						<span class="barcode-text">★ PINK-PONY-SPECIAL-PASS ★</span>
					</div>

					<div class="stub-footer">
						<span>МЕСТО: <strong>{purchasedTicket.seat}</strong></span>
						<span class="stamp-approved">ОДОБРЕНО РАДУГОЙ</span>
					</div>
				</div>
			</div>

			<div class="ticket-actions">
				<Button variant="secondary" onclick={() => window.print()}>
					{#snippet iconBefore()}<Printer size={14} />{/snippet}
					Распечатать билет
				</Button>
				<Button variant="primary" onclick={resetOrder}>
					{#snippet iconBefore()}<RotateCcw size={14} />{/snippet}
					Оформить еще один билет
				</Button>
			</div>
		</div>
	{/if}
</div>

<style>
	.pony-root {
		display: flex;
		flex-direction: column;
		gap: 1rem;
		font-family: inherit;
	}

	/* Hero Banner */
	.pony-hero-banner {
		position: relative;
		overflow: hidden;
		background: linear-gradient(135deg, #f472b6 0%, #ec4899 50%, #d946ef 100%);
		border-radius: var(--radius-md, 10px);
		padding: 1.5rem;
		color: #ffffff;
		box-shadow: 0 4px 20px rgba(236, 72, 153, 0.25);
	}

	.hero-content {
		position: relative;
		display: flex;
		align-items: center;
		gap: 1.25rem;
		z-index: 1;
	}

	.pony-icon-wrap {
		font-size: 3rem;
		background: rgba(255, 255, 255, 0.2);
		backdrop-filter: blur(8px);
		border-radius: 50%;
		width: 72px;
		height: 72px;
		display: flex;
		align-items: center;
		justify-content: center;
		box-shadow: 0 0 15px rgba(255, 255, 255, 0.4);
		flex-shrink: 0;
	}

	.hero-texts {
		flex: 1;
	}

	.hero-title-row {
		display: flex;
		justify-content: space-between;
		align-items: center;
		gap: 0.5rem;
		flex-wrap: wrap;
	}

	.hero-texts h2 {
		margin: 0;
		font-size: 1.25rem;
		font-weight: 800;
		letter-spacing: -0.01em;
		text-shadow: 0 1px 3px rgba(0,0,0,0.15);
	}
	.hero-texts p {
		margin: 0.35rem 0 0 0;
		font-size: 0.85rem;
		opacity: 0.95;
		line-height: 1.4;
		max-width: 750px;
	}

	.btn-hide-egg {
		display: inline-flex;
		align-items: center;
		gap: 0.3rem;
		background: rgba(0, 0, 0, 0.2);
		border: 1px solid rgba(255, 255, 255, 0.3);
		color: #ffffff;
		border-radius: 6px;
		padding: 0.25rem 0.55rem;
		font-size: 0.72rem;
		font-weight: 600;
		cursor: pointer;
		transition: all 0.15s ease;
	}
	.btn-hide-egg:hover {
		background: rgba(0, 0, 0, 0.35);
	}

	/* Booking Grid */
	.booking-grid {
		display: grid;
		grid-template-columns: 1.4fr 1fr;
		gap: 1rem;
	}

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

	/* Summary Card */
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

	/* Boarding Pass Ticket */
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
		.booking-grid {
			grid-template-columns: 1fr;
		}
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
