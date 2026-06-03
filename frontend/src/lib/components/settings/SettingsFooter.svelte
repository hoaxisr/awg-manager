<script lang="ts">
	import { usageLevel } from '$lib/stores/settings';
	import { GITHUB_BUG_REPORT_URL } from '$lib/utils/githubFeedback';

	const isExpert = $derived($usageLevel === 'expert');

	const genericIssueUrl = GITHUB_BUG_REPORT_URL;

	const credits = [
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
		'@2*****6@g*******m', '@S A', '@Ig**M**v***v', '@A Tu', '@metalnalks',
	];

	let open = $state(false);
</script>

<div class="settings-footer">
	<div class="settings-footer-bar">
		<span class="footer-link-group">
			Документация: <a href="https://awgm.hoaxisr.ru" target="_blank" rel="noopener noreferrer">awgm.hoaxisr.ru</a>
			<span class="footer-sep">·</span>
			<a href="/terms">Пользовательское соглашение</a>
			<span class="footer-sep">·</span>
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
			<span class="footer-sep">·</span>
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
				<span class="footer-sep">·</span>
				<a href="/api-docs">Swagger UI</a>
			{/if}
		</span>
		<button
			type="button"
			class="footer-collapse"
			class:open
			aria-expanded={open}
			onclick={() => (open = !open)}
		>
			<svg viewBox="0 0 24 24" width="14" height="14" fill="none" stroke="currentColor" stroke-width="2.5" stroke-linecap="round" stroke-linejoin="round">
				<path d="M9 6l6 6-6 6"/>
			</svg>
			<span class="footer-collapse-full">Благодарности ({credits.length})</span>
			<span class="footer-collapse-short">Благодарности</span>
		</button>
	</div>

	{#if open}
		<div class="card credits-card">
			<div class="credits-content">
				{#each credits as nick}
					<span class="credits-nick" class:gold={nick === '@amatol'} class:bronze={nick === '@tiffolk'}>{nick}</span>
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
	}

	.footer-link-group a {
		color: var(--color-accent);
		text-decoration: none;
	}

	.footer-link-group a:hover {
		text-decoration: underline;
	}

	.footer-sep {
		margin: 0 0.375rem;
		opacity: 0.4;
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
			flex-direction: column;
			align-items: stretch;
		}

		.footer-collapse {
			align-self: flex-end;
			text-transform: none;
			letter-spacing: normal;
			font-weight: 500;
		}

		.footer-collapse-full {
			display: none;
		}

		.footer-collapse-short {
			display: inline;
		}
	}

	.footer-collapse svg {
		transition: transform 0.15s ease;
	}

	.footer-collapse.open svg {
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
		background: var(--color-bg-primary);
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
