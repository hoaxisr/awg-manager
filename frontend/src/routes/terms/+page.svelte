<svelte:head>
	<title>Пользовательское соглашение - AWG Manager</title>
</svelte:head>

<div class="terms-page">
	<div class="terms-card">
		<h1>Пользовательское соглашение</h1>
		<p class="intro">
			<strong>AWG Manager</strong> — независимый open-source инструмент для управления AmneziaWG туннелями
			на роутерах Keenetic. Используя это ПО, вы принимаете условия ниже.
		</p>

		<section>
			<h2>1. Независимость</h2>
			<p>
				AWG Manager является независимым программным обеспечением и <strong>не аффилирован</strong> с компанией
				Keenetic, её партнёрами или лицензиарами. Все права на торговую марку «Keenetic», программное
				обеспечение и устройства Keenetic принадлежат их законным правообладателям.
			</p>
			<p>
				AWG Manager является независимым open-source проектом и <strong>не аффилирован</strong> с
				Amnezia.org, не является официальным продуктом или разработкой команды Amnezia.
			</p>
		</section>

		<section>
			<h2>2. Гарантии и ответственность</h2>
			<p>
				Программное обеспечение предоставляется <strong>«как есть»</strong> (as is), без каких-либо явных
				или подразумеваемых гарантий. Разработчики не несут ответственности за:
			</p>
			<ul>
				<li>повреждение устройства или потерю данных;</li>
				<li>потерю гарантии на оборудование;</li>
				<li>перебои в работе сети или деградацию сервисов;</li>
				<li>любые прямые или косвенные убытки, возникшие вследствие использования ПО.</li>
			</ul>
			<p>Вы используете программное обеспечение на собственный страх и риск.</p>
		</section>

		<section>
			<h2>3. Условия использования</h2>
			<p>Используя AWG Manager, вы подтверждаете, что:</p>
			<ul>
				<li>являетесь законным владельцем или администратором устройства, на котором установлено ПО;</li>
				<li>принимаете полную ответственность за конфигурацию и эксплуатацию;</li>
				<li>соблюдаете применимое законодательство вашей страны.</li>
			</ul>
		</section>

		<section>
			<h2>4. Назначение</h2>
			<p>
				AWG Manager разработан и используется исключительно в образовательных и исследовательских целях —
				для изучения сетевых технологий и протокола WireGuard.
			</p>
			<p>
				Данное программное обеспечение <strong>не является</strong> средством обхода блокировок, не
				предназначено для таких целей и <strong>не призывает</strong> к нарушению законодательства
				Российской Федерации или любой другой страны. Разработчики не пропагандируют и не рекламируют
				использование VPN или иных средств обхода ограничений доступа к информации.
			</p>
			<p>
				Пользователь самостоятельно несёт ответственность за соблюдение применимого законодательства
				при использовании данного ПО.
			</p>
		</section>

		<section>
			<h2>5. Техническая поддержка</h2>
			<p class="no-support">Техническая поддержка не осуществляется.</p>
			<p>Для решения всех проблем главная команда:</p>
			<pre><code>opkg remove awg-manager</code></pre>
			<p>
				Для получения сочувствия <span class="muted">(also known as посильная помощь)</span> необходимо
				предоставить:
			</p>
			<ol>
				<li>Логи уровня debug</li>
				<li>Подробное описание проблемы</li>
				<li>Версию ОС Keenetic и модель роутера</li>
				<li>Скриншот диагностики</li>
			</ol>

			<div class="faq">
				<div class="faq-item">
					<p class="faq-q">«Почему туннель не коннектится?»</p>
					<p class="faq-a">Решение гарантировано доступно на <a href="https://aviasales.ru" target="_blank" rel="noopener noreferrer">aviasales.ru</a>.</p>
				</div>
				<div class="faq-item">
					<p class="faq-q">«Вчера всё работало, а сегодня нет»</p>
					<p class="faq-a">
						Не касаясь политики: нужны технические подробности. Лечить болезнь печени
						по симптому «соседская собака ушла и не вернулась» не представляется возможным.
					</p>
				</div>
			</div>
		</section>

		<footer class="terms-footer">
			<a href="/" class="back-link">← Вернуться</a>
		</footer>
	</div>
</div>

<style>
	.terms-page {
		min-height: 100vh;
		display: flex;
		justify-content: center;
		padding: 2rem 1rem;
		background: var(--color-bg-primary);
	}

	.terms-card {
		width: 100%;
		max-width: 680px;
		padding: 2rem 2.5rem;
		background: var(--color-bg-secondary);
		border: 1px solid var(--color-border);
		border-radius: var(--radius);
		align-self: flex-start;
	}

	h1 {
		font-size: 1.5rem;
		font-weight: 700;
		margin: 0 0 0.75rem;
	}

	.intro {
		color: var(--color-text-secondary);
		font-size: 0.9375rem;
		margin-bottom: 2rem;
		line-height: 1.6;
	}

	section {
		margin-bottom: 1.75rem;
	}

	h2 {
		font-size: 1rem;
		font-weight: 600;
		margin: 0 0 0.625rem;
		color: var(--color-text);
	}

	p {
		font-size: 0.9rem;
		color: var(--color-text-secondary);
		line-height: 1.65;
		margin: 0 0 0.625rem;
	}

	ul, ol {
		margin: 0 0 0.625rem;
		padding-left: 1.5rem;
	}

	li {
		font-size: 0.9rem;
		color: var(--color-text-secondary);
		line-height: 1.65;
		margin-bottom: 0.25rem;
	}

	pre {
		background: var(--color-bg-primary);
		border: 1px solid var(--color-border);
		border-radius: var(--radius-sm);
		padding: 0.625rem 0.875rem;
		margin: 0.5rem 0 0.75rem;
		overflow-x: auto;
	}

	code {
		font-family: var(--font-mono, ui-monospace, monospace);
		font-size: 0.85rem;
		color: var(--color-text);
	}

	.no-support {
		font-weight: 600;
		color: var(--color-text) !important;
	}

	.muted {
		color: var(--color-text-muted);
		font-size: 0.85em;
	}

	.faq {
		margin-top: 1rem;
		display: flex;
		flex-direction: column;
		gap: 0.875rem;
	}

	.faq-item {
		border-left: 2px solid var(--color-border);
		padding-left: 0.875rem;
	}

	.faq-q {
		font-style: italic;
		font-weight: 500;
		color: var(--color-text) !important;
		margin-bottom: 0.25rem !important;
	}

	.faq-a {
		margin: 0 !important;
	}

	.faq-a a {
		color: var(--color-accent);
		text-decoration: none;
	}

	.faq-a a:hover {
		text-decoration: underline;
	}

	.terms-footer {
		margin-top: 2rem;
		padding-top: 1.25rem;
		border-top: 1px solid var(--color-border);
	}

	.back-link {
		font-size: 0.875rem;
		color: var(--color-accent);
		text-decoration: none;
	}

	.back-link:hover {
		text-decoration: underline;
	}

	@media (max-width: 640px) {
		.terms-card {
			padding: 1.5rem 1.25rem;
		}
	}
</style>
