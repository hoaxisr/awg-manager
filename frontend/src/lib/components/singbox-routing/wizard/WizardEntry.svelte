<script lang="ts">
	import { singboxWizard } from '$lib/stores/singboxWizard';

	function start(): void {
		singboxWizard.start();
	}
</script>

<div class="hero">
	<div class="head">Sing-box router пока не настроен</div>
	<div class="sub">
		Запустите мастер быстрой настройки — он за 4 шага создаст policy, привяжет устройства,
		применит выбранные пресеты и запустит движок.
		<br />
		Мастер использует реальные публичные rule-set источники SagerNet (файлы <code>.srs</code>),
		а не приватные URL подписок VLESS/VMess.
	</div>
	<button type="button" class="btn primary" onclick={start}>Запустить мастер</button>
</div>

<style>
	.hero {
		padding: 2.5rem 1.5rem;
		text-align: center;
		background: var(--color-bg-secondary);
		border: 1px dashed var(--color-border);
		border-radius: 8px;
	}
	.head {
		color: var(--color-text-primary);
		font-size: 1.1rem;
		font-weight: 600;
		margin-bottom: 0.6rem;
	}
	.sub {
		color: var(--color-text-muted);
		font-size: 0.88rem;
		max-width: 560px;
		margin: 0 auto 1.2rem;
		line-height: 1.5;
	}
	.btn { padding: 0.55rem 1.4rem; border-radius: 6px; font: inherit; cursor: pointer; border: 1px solid transparent; }
	.primary { color: white; background: #238636; border-color: #2ea043; }
</style>
