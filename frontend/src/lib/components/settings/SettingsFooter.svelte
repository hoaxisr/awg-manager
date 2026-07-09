<script lang="ts">
	import { usageLevel } from '$lib/stores/settings';
	import { GITHUB_BUG_REPORT_URL } from '$lib/utils/githubFeedback';
	import { ChevronRight } from 'lucide-svelte';

	const isExpert = $derived($usageLevel === 'expert');

	const genericIssueUrl = GITHUB_BUG_REPORT_URL;

	const credits = [
		'@dna_pvt',
		'@amatol',
		'@paris19891', '@The_Immortal', '@LionEvil', '@dio1122', '@Nidre',
		'@rexsniper', '@tiffolk', '@Shidla', '@palik_lelyakin', '@user_shurik',
		'@metasevss', '@reSigo', '@dnstkrv', '@JentRy', '@Il131',
		'@Gjkmpjdfntkm', '@NGC4563', '@NickHG55', '@moskinnickolas', '@antdocraf',
		'@primus_ultima', '@ninja1000sx70', '@neverny', '@ToDDiiN', '@vlzSilver',
		'@KomarovIgor', '@Skverna84', '@SBogolyubov', '@Kub26', '@kentbrokeman',
		'@Sergej_Kopyshev', '@Green_snakee', '@Console4ka', '@Vorlam Vorlamov', '@ras****.com',
		'@Даниил_***ов', '@White3d3', '@neoplazma', '@Борис_Д******о', '@N1KN0',
		'@GregMSK', '@vadim_uv', '@xProtosx', '@RaggaSimpson', '@М****л Л*****о',
		'@D***s C************o', '@MrUndefined86', '@А*******р Ч******н', '@vkh_ent', '@momomol777',
		'@Grimrade', '@N0news', '@zixstass', '@Agenstily', '@Russlan_89',
		'@fr0z3n_rzr', '@boris_e', '@Vgjkuj', '@tkrv09', '@Sadrutdin',
		'@Clawa1984', '@beautiful_lion', '@Link_Sergey', '@Dannis_CH', '@vano_milah',
		'@VylenSV', '@verbee09', '@EfimovYuriy', '@vumaximov', '@Maximus',
		'@VlZlVlZ', '@sergeinesl', '@unclownartist', '@game47', '@A_Valerich',
		'@Влад*** С***н', '@Space_Voyager_Telegram', '@Brown2Fox', '@Mr_SiB', '@Anch665',
		'@TorTik59', '@kdeveloper', '@genomedon', '@byVladimirB', '@Jona_home',
		'@voveg', '@Vitaly', '@Dude_47', '@aleksandr_nurov', '@sincezver',
		'@Novosat', '@Evgenii', '@ayastrebov', '@dany_massiv', '@Litvix',
		'@Evko', '@gen****@m****u', '@Frinstall', '@ev**************y@g*******m', '@IARESI',
		'@ig*****@g*******m', '@Да**** Т***в', '@vi*****@g*******.m', '@ku*******@g*******m',
		'@d****8@y***u', '@PolarPriest', '@augin', '@me***-***r@y***u', '@Byrnane',
		'@a******r@g*******m', '@k*******7@g*******m', '@Лохматая Чупакабра', '@Proxy', '@DELETED',
		'@2*****6@g*******m', '@S A', '@Ig**M**v***v', '@A Tu', '@metalnakls',
		'@LazIv', '@AverTV', '@xxxaaach', '@defylives', '@AndreyPristup',
		'@Lethal F',
		'@Stein_123', '@Tanovitsky',
	];

	let open = $state(false);
</script>

<div class="settings-footer">
	<div class="settings-footer-bar">
		<div class="footer-link-group">
			<div class="footer-doc-line">
				<span class="footer-doc-label">Документация:</span>
				<a href="https://awgm.hoaxisr.ru" target="_blank" rel="noopener noreferrer">awgm.hoaxisr.ru</a>
			</div>
			<div class="footer-links-line">
				<a href="/terms">Пользовательское соглашение</a>
				<span class="footer-sep" aria-hidden="true">·</span>
				<a
					class="github-link"
					href="https://github.com/hoaxisr/awg-manager"
					target="_blank"
					rel="noopener noreferrer"
					aria-label="Открыть GitHub репозиторий AWG Manager"
					title="GitHub репозиторий AWG Manager"
				>
					GitHub
				</a>
				<span class="footer-sep" aria-hidden="true">·</span>
				<a
					href={genericIssueUrl}
					target="_blank"
					rel="noopener noreferrer"
					aria-label="Открыть форму GitHub issue для обратной связи"
					title="Публичный GitHub issue: это не служба поддержки"
				>
					Сообщить о проблеме
				</a>
				{#if isExpert}
					<span class="footer-sep" aria-hidden="true">·</span>
					<a href="/api-docs">Swagger UI</a>
				{/if}
			</div>
		</div>
		<button
			type="button"
			class="footer-collapse"
			class:open
			aria-expanded={open}
			onclick={() => (open = !open)}
		>
			<span class="footer-icon" class:open aria-hidden="true"><ChevronRight size={14} strokeWidth={2.5} /></span>
			<span class="footer-collapse-full">Благодарности ({credits.length})</span>
			<span class="footer-collapse-short">Благодарности</span>
		</button>
	</div>

	{#if open}
		<div class="card credits-card">
			<div class="credits-content">
				{#each credits as nick}
					<span class="credits-nick" class:gold={nick === '@dna_pvt' || nick === '@amatol'} class:bronze={nick === '@tiffolk' || nick === '@defylives'}>{nick}</span>
				{/each}
			</div>
		</div>
	{/if}
</div>

<style>
	.settings-footer {
		display: flex;
		flex-direction: column;
		gap: 0.5rem;
	}

	.settings-footer-bar {
		display: flex;
		justify-content: space-between;
		align-items: center;
		gap: 0.5rem;
		padding: 0.625rem 0.875rem;
		background: var(--color-bg-secondary);
		border: 1px solid var(--color-border);
		border-radius: var(--radius);
		font-size: 0.8125rem;
		color: var(--color-text-secondary);
	}

	.footer-link-group {
		min-width: 0;
		flex: 1 1 auto;
		display: flex;
		flex-direction: column;
		gap: 0.125rem;
	}

	.footer-doc-line,
	.footer-links-line {
		min-width: 0;
	}

	.footer-doc-line {
		display: flex;
		flex-wrap: wrap;
		gap: 0.25rem;
	}

	.footer-doc-label {
		color: var(--color-text-secondary);
	}

	.footer-links-line {
		display: flex;
		flex-direction: column;
		align-items: flex-start;
		gap: 0.125rem;
	}

	.footer-sep {
		display: none;
	}

	.footer-link-group a {
		color: var(--color-accent);
		text-decoration: none;
	}

	.footer-link-group a:hover {
		text-decoration: underline;
	}

	@media (min-width: 700px) {
		.footer-links-line {
			flex-flow: row wrap;
			align-items: center;
			gap: 0;
			row-gap: 0.125rem;
		}

		.footer-sep {
			display: inline;
			margin: 0 0.375rem;
			opacity: 0.4;
		}
	}

	.footer-collapse {
		display: inline-flex;
		align-items: center;
		gap: 0.375rem;
		flex-shrink: 0;
		background: none;
		border: none;
		color: var(--color-text-muted);
		font-size: 0.75rem;
		text-transform: uppercase;
		letter-spacing: 0.05em;
		font-weight: 600;
		cursor: pointer;
		padding: 0;
	}

	.footer-collapse-short {
		display: none;
	}

	@media (max-width: 640px) {
		.settings-footer-bar {
			display: grid;
			grid-template-columns: minmax(0, 1fr) auto;
			align-items: end;
			column-gap: 0.75rem;
			row-gap: 0.375rem;
		}

		.footer-collapse {
			align-self: end;
			justify-self: end;
			text-transform: none;
			letter-spacing: normal;
			font-weight: 500;
		}

		.footer-link-group {
			gap: 0.25rem;
		}

		.footer-doc-line,
		.footer-links-line {
			line-height: 1.45;
		}

		.footer-collapse-full {
			display: none;
		}

		.footer-collapse-short {
			display: inline;
		}
	}

	.footer-icon {
		display: inline-flex;
		transition: transform 0.15s ease;
	}

	.footer-icon.open {
		transform: rotate(90deg);
	}

	.credits-card {
		padding: 0.875rem;
	}

	.credits-content {
		display: flex;
		flex-wrap: wrap;
		gap: 0.375rem;
	}

	.credits-nick {
		font-size: 0.75rem;
		font-family: var(--font-mono);
		color: var(--color-text-muted);
		background: var(--color-settings-control-bg);
		padding: 0.125rem 0.5rem;
		border-radius: 10px;
		border: 1px solid var(--color-border);
	}

	.credits-nick.gold {
		color: #d4af37;
		font-weight: 700;
		background: rgba(212, 175, 55, 0.12);
		border-color: rgba(212, 175, 55, 0.7);
	}

	.credits-nick.bronze {
		color: #cd7f32;
		font-weight: 700;
		background: rgba(205, 127, 50, 0.12);
		border-color: rgba(205, 127, 50, 0.7);
	}
</style>
