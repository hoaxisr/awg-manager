<script lang="ts">
	import { EyeOff } from 'lucide-svelte';

	interface Props {
		onhide: () => void;
	}

	let { onhide }: Props = $props();
</script>

<div class="pony-hero-banner">
	<div class="hero-content">
		<div class="pony-icon-wrap">
			<span class="emoji-huge">🦄</span>
		</div>
		<div class="hero-texts">
			<div class="hero-title-row">
				<h2>Касса межгалактических экспрессов «Розовый Пони»</h2>
				<button type="button" class="btn-hide-egg" title="Спрятать эту вкладку обратно" onclick={onhide}>
					<EyeOff size={13} />
					<span>Спрятать пасхалку</span>
				</button>
			</div>
			<p>Секретный раздел для тех, кто устал от конфигураций и туннелей. Здесь интернет свободен, а роутеры никогда не зависают!</p>
		</div>
	</div>
</div>

<style>
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
</style>
