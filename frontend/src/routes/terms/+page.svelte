<svelte:head>
	<title>Пользовательское соглашение - AWGM</title>
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
				AWG Manager разработан и используется в образовательных и исследовательских целях, в том числе —
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
			<h2>5. Интеграции</h2>
			<p>
				В AWG Manager могут присутствовать страницы и элементы управления для стороннего ПО, устанавливаемого и/или 
				установленного на устройстве пользователя отдельно и независимо от AWG Manager. Разработчики AWG Manager
				<strong>не разрабатывают, не распространяют, не поддерживают и не несут ответственности</strong>
				за такое стороннее ПО, его работоспособность, совместимость, обновления и последствия
				использования.
			</p>
			<p>
				К вышеупомянутому, также относятся гео-файлы (geoip и geosite), списки доменов в формате srs, 
				списки подсетей, и прочие данные, предоставляемые сторонними сервисами.
			</p>
			<p>
				Указанные элементы — являются <strong>исключительно интерфейсом</strong> для удобства администрирования
				уже установленных у вас компонентов. Они не расширяют функциональность AWG Manager и не
				означают одобрения, партнёрства или гарантий в отношении сторонних продуктов.
			</p>
			<p>
				Вопросы о работе, сбоях, настройке и ожидаемом поведении конкретного стороннего функционала
				направляйте <strong>разработчикам и сопровождающим этого ПО</strong>, а не команде AWG Manager.
				Если вы не понимаете, как устроена интеграция или что делает сторонний компонент, —
				<strong>не используйте её</strong> и не ожидайте разъяснений или исправлений со стороны AWG
				Manager: мы здесь ни при чём.
			</p>
		</section>

		<section>
			<h2>6. Технические рекомендации и предосторожности</h2>
			<p>
				AWG Manager может изменять настройки роутера, сети и связанных компонентов. Перед любыми
				действиями в интерфейсе <strong>рекомендуем сделать резервную копию</strong> конфигурации
				роутера (NDMS) и/или Entware.
			</p>
			<p>
				Сам AWG Manager <strong>не передаёт</strong> ваши ключи, конфигурации и прочую
				конфиденциальную информацию за пределы устройства. Если вы выгружаете логи, отчёты
				диагностики или иные данные и отправляете их кому-либо — делаете это
				<strong>на свой страх и риск</strong>.
			</p>
			<p>
				<strong>Не отправляйте конфигурации туннелей третьим лицам.</strong> Если всё же
				отправили — последствия (утечка ключей, перехват трафика и т.п.) целиком на вашей ответственности.
			</p>
			<p>
				Импорт чужих подписок, конфигов из непроверенных источников и любого подобного контента —
				на вашей ответственности. Мы не проверяем, куда уйдёт трафик и что скрыто внутри файла,
				сохранность вашей конфиденциальности при подобном импорте на вашей ответственности.
			</p>
			<p>
				При обновлении AWG Manager через opkg или иным способом
				<strong>не гарантируется</strong>, что прежняя схема и настройки продолжат работать как
				раньше. То, что работало на предыдущей версии, после обновления может перестать — и это не повод для
				претензий к разработчикам.
			</p>
		</section>

		<section>
			<h2>7. Техническая поддержка</h2>
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
				<li>Скриншот страницы окружения</li>
				<li>Диагностический отчёт</li>
				<li style="opacity: 0.5;">Уважение к команде AWG Manager и сообществу (было бы неплохо)</li>
			</ol>
			<p>И если летом выпадет снег, на горе свистнет рак, а меркурий не ретроградный, участники сообщества, вероятно, поспорят о причинах, но не факт, что помогут вам решить проблему.</p>

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

		<div class="acceptance-banner" role="note">
			<p>
				Продолжая пользоваться AWG Manager, в том числе имея пакет <code>awg-manager</code> установленным
				на своём роутере, вы подтверждаете, что прочитали и <strong>соглашаетесь</strong> с настоящим
				пользовательским соглашением.
			</p>
		</div>

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

	.terms-card ul,
	.terms-card ol {
		margin: 0 0 0.625rem;
		padding-left: 1.75rem;
		list-style-position: outside;
	}

	.terms-card ul {
		list-style-type: disc;
	}

	.terms-card ol {
		list-style-type: decimal;
	}

	.terms-card li {
		font-size: 0.9rem;
		color: var(--color-text-secondary);
		line-height: 1.65;
		margin-bottom: 0.25rem;
		padding-left: 0.25rem;
	}

	.terms-card li::marker {
		color: var(--color-text-muted);
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

	.acceptance-banner {
		margin-top: 0.5rem;
		margin-bottom: 1.75rem;
		padding: 1rem 1.125rem;
		background: var(--color-bg-primary);
		border: 1px solid var(--color-accent);
		border-radius: var(--radius-sm);
	}

	.acceptance-banner p {
		margin: 0;
		font-size: 0.9rem;
		color: var(--color-text);
		line-height: 1.65;
	}

	.acceptance-banner code {
		font-size: 0.85em;
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
