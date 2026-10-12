<script lang="ts">
	import { m } from '$lib/i18n';
	export type PukhososAnimation = 'idle' | 'walk' | 'turn';
	export type PukhososWalkFrameMode = 'cycle' | 'sequence';

	import {
		createWalkFrameSequencer,
		PUKHOSOS_SPRITE_TURN_CYCLE_MS,
		PUKHOSOS_SPRITE_WALK_CYCLE_MS,
		PUKHOSOS_SPRITE_WALK_FRAME_MS,
		PUKHOSOS_WALK_FRAME_MODE,
	} from '$lib/utils/pukhososPatrol';

	interface Props {
		/** idle — стоит; walk — цикл движения; turn — разворот (один раз). */
		animation?: PukhososAnimation;
		/** Куда смотрит пухосос после анимации. */
		facing?: 'left' | 'right';
		/** Ширина одного кадра в px. */
		size?: number;
		/** cycle — кадры по порядку; sequence — секвенсы приоритет / подпрыг. */
		walkMode?: PukhososWalkFrameMode;
		/** Длительность цикла ходьбы (7 кадров), мс. Только walkMode === 'cycle'. */
		walkCycleMs?: number;
		/** Интервал смены кадра, мс. Только walkMode === 'sequence'. */
		walkFrameMs?: number;
		/** Длительность разворота (8 кадров), мс. */
		turnCycleMs?: number;
		/** Вызывается по окончании turn. */
		onTurnComplete?: () => void;
	}

	/** Ячейка спрайтмапа: 280×200, сетка 8×5. */
	const FRAME_ASPECT = 200 / 280;

	let {
		animation = 'idle',
		facing = 'right',
		size = 128,
		walkMode = PUKHOSOS_WALK_FRAME_MODE,
		walkCycleMs = PUKHOSOS_SPRITE_WALK_CYCLE_MS,
		walkFrameMs = PUKHOSOS_SPRITE_WALK_FRAME_MS,
		turnCycleMs = PUKHOSOS_SPRITE_TURN_CYCLE_MS,
		onTurnComplete,
	}: Props = $props();

	const height = $derived(Math.round(size * FRAME_ASPECT));
	const walkLeft = $derived(animation === 'walk' && facing === 'left');
	const turnRight = $derived(animation === 'turn' && facing === 'right');
	const walkSequenced = $derived(animation === 'walk' && walkMode === 'sequence');

	let sequencedWalkBg = $state<string | null>(null);

	function walkFramePosition(col: number, dir: 'left' | 'right') {
		const x = (col / 7) * 100;
		const y = dir === 'left' ? 25 : 0;
		return `${x}% ${y}%`;
	}

	$effect(() => {
		if (!walkSequenced) {
			sequencedWalkBg = null;
			return;
		}

		const dir = facing;
		const sequencer = createWalkFrameSequencer();

		const showFrame = () => {
			const col = sequencer.next();
			sequencedWalkBg = walkFramePosition(col, dir);
		};

		showFrame();
		const timer = setInterval(showFrame, walkFrameMs);
		return () => clearInterval(timer);
	});
</script>

<div class="pukhosos-shell">
	<div
		class="pukhosos"
		class:idle={animation === 'idle'}
		class:idle-left={animation === 'idle' && facing === 'left'}
		class:walk-right={animation === 'walk' && walkMode === 'cycle' && facing === 'right'}
		class:walk-left={animation === 'walk' && walkMode === 'cycle' && walkLeft}
		class:walk-sequenced={walkSequenced}
		class:turn-left={animation === 'turn' && facing === 'left'}
		class:turn-right={turnRight}
		style:--frame-w="{size}px"
		style:--frame-h="{height}px"
		style:--walk-cycle="{walkCycleMs}ms"
		style:--turn-cycle="{turnCycleMs}ms"
		style:background-position={walkSequenced && sequencedWalkBg ? sequencedWalkBg : undefined}
		role="img"
		aria-label={m.settings_experimental_vacuum_label()}
		onanimationend={(event) => {
			if (
				animation === 'turn' &&
				(event.animationName === 'pukhosos-turn-left' ||
					event.animationName === 'pukhosos-turn-right')
			) {
				onTurnComplete?.();
			}
		}}
	></div>
</div>

<style>
	.pukhosos {
		--sprite-cols: 8;
		--sprite-rows: 5;
		width: var(--frame-w);
		height: var(--frame-h);
		flex-shrink: 0;
		background-repeat: no-repeat;
		background-image: url('/sprites/pukhosos-spritemap.png');
		background-size: calc(var(--sprite-cols) * 100%) calc(var(--sprite-rows) * 100%);
		background-position: 0% 100%;
		image-rendering: pixelated;
		image-rendering: crisp-edges;
	}

	.pukhosos-shell {
		display: inline-flex;
		align-items: flex-end;
	}

	.pukhosos.idle {
		background-position: 0% 100%;
	}

	.pukhosos.idle.idle-left {
		background-position: 14.286% 100%;
	}

	.pukhosos.walk-right {
		animation: pukhosos-walk-right var(--walk-cycle) steps(1) infinite;
	}

	.pukhosos.walk-left {
		animation: pukhosos-walk-left var(--walk-cycle) steps(1) infinite;
	}

	.pukhosos.walk-sequenced {
		animation: none;
	}

	.pukhosos.turn-left {
		animation: pukhosos-turn-left var(--turn-cycle) steps(1) forwards;
	}

	.pukhosos.turn-right {
		animation: pukhosos-turn-right var(--turn-cycle) steps(1) forwards;
	}

	@keyframes pukhosos-walk-right {
		0% {
			background-position: 0% 0%;
		}
		14.3% {
			background-position: 14.286% 0%;
		}
		28.6% {
			background-position: 28.571% 0%;
		}
		42.9% {
			background-position: 42.857% 0%;
		}
		57.1% {
			background-position: 57.143% 0%;
		}
		71.4% {
			background-position: 71.429% 0%;
		}
		85.7% {
			background-position: 85.714% 0%;
		}
		100% {
			background-position: 0% 0%;
		}
	}

	@keyframes pukhosos-walk-left {
		0% {
			background-position: 0% 25%;
		}
		14.3% {
			background-position: 14.286% 25%;
		}
		28.6% {
			background-position: 28.571% 25%;
		}
		42.9% {
			background-position: 42.857% 25%;
		}
		57.1% {
			background-position: 57.143% 25%;
		}
		71.4% {
			background-position: 71.429% 25%;
		}
		85.7% {
			background-position: 85.714% 25%;
		}
		100% {
			background-position: 0% 25%;
		}
	}

	@keyframes pukhosos-turn-left {
		0% {
			background-position: 0% 50%;
		}
		12.5% {
			background-position: 14.286% 50%;
		}
		25% {
			background-position: 28.571% 50%;
		}
		37.5% {
			background-position: 42.857% 50%;
		}
		50% {
			background-position: 57.143% 50%;
		}
		62.5% {
			background-position: 71.429% 50%;
		}
		75% {
			background-position: 85.714% 50%;
		}
		87.5% {
			background-position: 100% 50%;
		}
		100% {
			background-position: 100% 50%;
		}
	}

	@keyframes pukhosos-turn-right {
		0% {
			background-position: 0% 75%;
		}
		12.5% {
			background-position: 14.286% 75%;
		}
		25% {
			background-position: 28.571% 75%;
		}
		37.5% {
			background-position: 42.857% 75%;
		}
		50% {
			background-position: 57.143% 75%;
		}
		62.5% {
			background-position: 71.429% 75%;
		}
		75% {
			background-position: 85.714% 75%;
		}
		87.5% {
			background-position: 100% 75%;
		}
		100% {
			background-position: 100% 75%;
		}
	}
</style>
