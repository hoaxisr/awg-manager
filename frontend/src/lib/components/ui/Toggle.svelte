<script lang="ts" module>
    export type ToggleSpinnerPosition = 'before' | 'after' | 'none';
    /** Status tint for flip toggles (AWG tunnel cards / list). */
    export type ToggleTint = 'recovering' | 'starting' | 'unreachable';
</script>

<script lang="ts">
    interface Props {
        checked: boolean;
        onchange: (checked: boolean) => void;
        loading?: boolean;
        disabled?: boolean;
        label?: string;
        hint?: string;
        /** Accessible name for the checkbox input — for toggles without a visible label. */
        ariaLabel?: string;
        size?: 'sm' | 'md';
        variant?: 'slider' | 'flip';
        /** Flip ON-state colour override (AWG recovering / starting / unreachable). */
        tint?: ToggleTint;
        /** Spinner slot relative to the slider track; `none` — без слота (flip variant ignores this). */
        spinner?: ToggleSpinnerPosition;
        // controlled: parent owns the state. The toggle does NOT self-commit
        // the click — it reverts the DOM to `checked` and lets the parent
        // drive the value via onchange. Needed when onchange defers the change
        // (e.g. behind a confirm modal): on cancel the prop never changes, so
        // the toggle must not stay visually flipped. Default keeps the legacy
        // optimistic behaviour (and bind:checked support).
        controlled?: boolean;
    }

    let {
        checked = $bindable(),
        onchange,
        loading = false,
        disabled = false,
        label = '',
        hint = '',
        ariaLabel = '',
        size = 'md',
        variant = 'slider',
        tint,
        spinner = 'before',
        controlled = false,
    }: Props = $props();

    // Видимая подпись лежит вне .toggle-container (она рядом с дорожкой, а не
    // внутри неё), поэтому связь с чекбоксом — через for/id: иначе клик по
    // тексту ничего не переключает, а у поля нет доступного имени.
    const fieldId = $props.id();

    function handleInput(event: Event) {
        if (loading || disabled) {
            event.preventDefault();
            return;
        }
        const input = event.currentTarget as HTMLInputElement;
        const nextChecked = input.checked;
        if (controlled) {
            // Revert the browser's optimistic flip; the parent re-renders
            // `checked` only if it accepts the change.
            input.checked = checked;
            if (onchange) onchange(nextChecked);
            return;
        }
        checked = nextChecked;
        if (onchange) onchange(nextChecked);
    }
</script>

{#if label}
    <div class="toggle-group">
        <label
            class="toggle-container"
            class:loading
            class:sm={size === 'sm'}
            class:flip={variant === 'flip'}
            class:tint-recovering={tint === 'recovering'}
            class:tint-starting={tint === 'starting'}
            class:tint-unreachable={tint === 'unreachable'}
        >
            <input type="checkbox" id={fieldId} checked={checked} {disabled} aria-label={ariaLabel || undefined} oninput={handleInput} />
            {#if variant === 'flip'}
                <span class="flip-track">
                    <span class="flip-lever">
                        {#if loading}
                            <span class="flip-spinner"></span>
                        {/if}
                    </span>
                </span>
            {:else if spinner === 'after'}
                <span class="toggle-slider"></span>
                <span class="toggle-spinner-slot" aria-hidden="true">
                    {#if loading}<span class="toggle-spinner"></span>{/if}
                </span>
            {:else if spinner === 'none'}
                <span class="toggle-slider"></span>
            {:else}
                <span class="toggle-spinner-slot" aria-hidden="true">
                    {#if loading}<span class="toggle-spinner"></span>{/if}
                </span>
                <span class="toggle-slider"></span>
            {/if}
        </label>
        <div class="toggle-text">
            <label class="toggle-label" for={fieldId}>{label}</label>
            {#if hint}
                <span class="toggle-hint">{hint}</span>
            {/if}
        </div>
    </div>
{:else}
    <label
        class="toggle-container"
        class:loading
        class:sm={size === 'sm'}
        class:flip={variant === 'flip'}
        class:tint-recovering={tint === 'recovering'}
        class:tint-starting={tint === 'starting'}
        class:tint-unreachable={tint === 'unreachable'}
    >
        <input type="checkbox" checked={checked} {disabled} aria-label={ariaLabel || undefined} oninput={handleInput} />
        {#if variant === 'flip'}
            <span class="flip-track">
                <span class="flip-lever">
                    {#if loading}
                        <span class="flip-spinner"></span>
                    {/if}
                </span>
            </span>
        {:else if spinner === 'after'}
            <span class="toggle-slider"></span>
            <span class="toggle-spinner-slot" aria-hidden="true">
                {#if loading}<span class="toggle-spinner"></span>{/if}
            </span>
        {:else if spinner === 'none'}
            <span class="toggle-slider"></span>
        {:else}
            <span class="toggle-spinner-slot" aria-hidden="true">
                {#if loading}<span class="toggle-spinner"></span>{/if}
            </span>
            <span class="toggle-slider"></span>
        {/if}
    </label>
{/if}

<style>
    .toggle-container {
        position: relative;
        display: inline-flex;
        align-items: center;
        gap: 8px;
        cursor: pointer;
        vertical-align: middle;
        margin-bottom: 0;
    }

    /* Reserved slot next to the slider — prevents layout jump between
       idle and loading. Empty in idle, spinner appears when loading. */
    .toggle-spinner-slot {
        width: 12px;
        height: 12px;
        display: inline-flex;
        align-items: center;
        justify-content: center;
        flex-shrink: 0;
    }

    .toggle-container input {
        position: absolute;
        opacity: 0;
        width: 0;
        height: 0;
    }

    /* ===== Slider variant (default) ===== */

    .toggle-slider {
        position: relative;
        width: 44px;
        height: 24px;
        background: var(--color-bg-tertiary);
        border-radius: var(--radius);
        transition: background 0.2s ease;
    }

    .toggle-slider::before {
        content: '';
        position: absolute;
        top: 2px;
        left: 2px;
        width: 20px;
        height: 20px;
        background: var(--color-text-muted);
        border: 1px solid color-mix(in srgb, var(--color-text-primary) 10%, transparent);
        border-radius: 50%;
        box-shadow:
            0 1px 2px rgba(0, 0, 0, 0.28),
            inset 0 1px 0 rgba(255, 255, 255, 0.08);
        transition:
            transform 0.2s ease,
            background 0.2s ease,
            border-color 0.2s ease,
            box-shadow 0.2s ease;
    }

    .toggle-container input:checked ~ .toggle-slider {
        background: var(--color-accent);
    }

    .toggle-container input:checked ~ .toggle-slider::before {
        transform: translateX(20px);
        background: color-mix(in srgb, #fffdf2 92%, var(--color-bg-primary) 8%);
        border-color: color-mix(in srgb, var(--color-text-primary) 20%, transparent);
        box-shadow:
            0 1px 3px rgba(0, 0, 0, 0.34),
            0 0 0 1px color-mix(in srgb, var(--color-text-primary) 8%, transparent),
            inset 0 1px 0 rgba(255, 255, 255, 0.45);
    }

    /* Neo: жёлтый трек — почти белый бегунок теряется; затемняем в обеих ветках пресета */
    :global(html[data-theme-preset='neo']) .toggle-container input:checked ~ .toggle-slider::before {
        background: color-mix(in srgb, #8a8000 48%, var(--color-bg-primary) 52%);
        border-color: color-mix(in srgb, var(--color-text-primary) 32%, transparent);
        box-shadow:
            0 1px 3px rgba(0, 0, 0, 0.42),
            0 0 0 1px color-mix(in srgb, var(--color-text-primary) 14%, transparent),
            inset 0 1px 0 rgba(255, 255, 255, 0.22);
    }

    .toggle-container:hover .toggle-slider {
        background: var(--color-border);
    }

    .toggle-container input:checked:hover ~ .toggle-slider {
        filter: brightness(1.1);
    }

    /* Small variant */
    .toggle-container.sm .toggle-slider {
        width: 32px;
        height: 18px;
        border-radius: 9px;
    }

    .toggle-container.sm .toggle-slider::before {
        width: 14px;
        height: 14px;
        top: 2px;
        left: 2px;
    }

    .toggle-container.sm input:checked ~ .toggle-slider::before {
        transform: translateX(14px);
    }

    /* ===== Flip switch variant ===== */

    .flip-track {
        position: relative;
        width: 26px;
        height: 42px;
        background: var(--color-bg-tertiary);
        border-radius: var(--radius-sm);
        box-shadow:
            inset 0 2px 4px rgba(0, 0, 0, 0.3),
            inset 0 -1px 2px rgba(255, 255, 255, 0.05);
        transition: background 0.4s ease, box-shadow 0.4s ease;
        overflow: hidden;
    }

    .flip-lever {
        position: absolute;
        left: 3px;
        bottom: 3px;
        width: 20px;
        height: 20px;
        background: linear-gradient(
            to bottom,
            color-mix(in srgb, var(--color-text-muted) 80%, white),
            var(--color-text-muted)
        );
        border-radius: var(--radius-sm);
        box-shadow:
            0 1px 3px rgba(0, 0, 0, 0.3),
            inset 0 1px 0 rgba(255, 255, 255, 0.1);
        transition: transform 0.2s ease, background 0.4s ease, box-shadow 0.4s ease;
        display: flex;
        align-items: center;
        justify-content: center;
    }

    /* Lever ridge (tactile line) */
    .flip-lever::before {
        content: '';
        width: 10px;
        height: 2px;
        background: rgba(255, 255, 255, 0.15);
        border-radius: 1px;
    }

    /* ON state: lever at top */
    .toggle-container.flip input:checked + .flip-track {
        background: var(--color-success-tint);
        box-shadow:
            inset 0 2px 4px rgba(0, 0, 0, 0.2),
            inset 0 -1px 2px var(--color-success-tint),
            0 0 8px var(--color-success-tint);
    }

    .toggle-container.flip input:checked + .flip-track .flip-lever {
        transform: translateY(-16px);
        background: linear-gradient(
            to bottom,
            color-mix(in srgb, var(--color-success) 75%, white),
            var(--color-success)
        );
        box-shadow:
            0 1px 3px rgba(0, 0, 0, 0.3),
            0 0 6px var(--color-success-border),
            inset 0 1px 0 rgba(255, 255, 255, 0.2);
    }

    .toggle-container.flip input:checked + .flip-track .flip-lever::before {
        background: rgba(255, 255, 255, 0.3);
    }

    .toggle-container.flip.loading .flip-lever::before {
        display: none;
    }

    /* Hover */
    .toggle-container.flip:hover .flip-lever {
        filter: brightness(1.15);
    }

    /* ===== Horizontal flip (size="sm" + variant="flip") ===== */

    .toggle-container.sm.flip .flip-track {
        width: 32px;
        height: 18px;
        border-radius: var(--radius-sm);
        box-shadow:
            inset 2px 0 4px rgba(0, 0, 0, 0.28),
            inset -1px 0 2px rgba(255, 255, 255, 0.04);
    }

    .toggle-container.sm.flip .flip-lever {
        left: 3px;
        bottom: 3px;
        width: 12px;
        height: 12px;
        border-radius: calc(var(--radius-sm) - 1px);
    }

    /* ridge — вертикальная полоска для горизонтального рычага */
    .toggle-container.sm.flip .flip-lever::before {
        width: 2px;
        height: 6px;
        background: rgba(255, 255, 255, 0.15);
        border-radius: 1px;
    }

    /* ON: рычаг едет вправо */
    .toggle-container.sm.flip input:checked + .flip-track .flip-lever {
        transform: translateX(14px);
    }

    /* ON: трек зеленеет */
    .toggle-container.sm.flip input:checked + .flip-track {
        box-shadow:
            inset 2px 0 4px rgba(0, 0, 0, 0.15),
            inset -1px 0 2px var(--color-success-tint),
            0 0 6px var(--color-success-tint);
    }

    /* AWG status tints — must beat default green ON styles above */
    .toggle-container.tint-recovering.flip input:checked + .flip-track,
    .toggle-container.tint-recovering.sm.flip input:checked + .flip-track {
        background: color-mix(in srgb, var(--color-broken) 18%, var(--color-bg-tertiary));
        box-shadow:
            inset 2px 0 4px rgba(0, 0, 0, 0.18),
            0 0 6px color-mix(in srgb, var(--color-broken) 35%, transparent);
    }

    .toggle-container.tint-recovering.flip input:checked + .flip-track .flip-lever,
    .toggle-container.tint-recovering.sm.flip input:checked + .flip-track .flip-lever {
        background: linear-gradient(
            to bottom,
            color-mix(in srgb, var(--color-broken) 75%, white),
            var(--color-broken)
        );
        box-shadow:
            0 1px 3px rgba(0, 0, 0, 0.3),
            0 0 5px color-mix(in srgb, var(--color-broken) 45%, transparent);
    }

    .toggle-container.tint-starting.flip input:checked + .flip-track,
    .toggle-container.tint-starting.sm.flip input:checked + .flip-track {
        background: color-mix(in srgb, var(--color-warning) 18%, var(--color-bg-tertiary));
        box-shadow:
            inset 2px 0 4px rgba(0, 0, 0, 0.18),
            0 0 6px color-mix(in srgb, var(--color-warning) 35%, transparent);
    }

    .toggle-container.tint-starting.flip input:checked + .flip-track .flip-lever,
    .toggle-container.tint-starting.sm.flip input:checked + .flip-track .flip-lever {
        background: linear-gradient(
            to bottom,
            color-mix(in srgb, var(--color-warning) 75%, white),
            var(--color-warning)
        );
        box-shadow:
            0 1px 3px rgba(0, 0, 0, 0.3),
            0 0 5px color-mix(in srgb, var(--color-warning) 45%, transparent);
    }

    .toggle-container.tint-unreachable.flip input:checked + .flip-track,
    .toggle-container.tint-unreachable.sm.flip input:checked + .flip-track {
        background: color-mix(in srgb, var(--color-error) 18%, var(--color-bg-tertiary));
        box-shadow:
            inset 2px 0 4px rgba(0, 0, 0, 0.18),
            0 0 6px color-mix(in srgb, var(--color-error) 35%, transparent);
    }

    .toggle-container.tint-unreachable.flip input:checked + .flip-track .flip-lever,
    .toggle-container.tint-unreachable.sm.flip input:checked + .flip-track .flip-lever {
        background: linear-gradient(
            to bottom,
            color-mix(in srgb, var(--color-error) 75%, white),
            var(--color-error)
        );
        box-shadow:
            0 1px 3px rgba(0, 0, 0, 0.3),
            0 0 5px color-mix(in srgb, var(--color-error) 45%, transparent);
    }

    .toggle-container.sm.flip .flip-spinner {
        width: 7px;
        height: 7px;
        border-width: 1.5px;
    }

    /* ===== Loading state ===== */

    .toggle-container.loading {
        pointer-events: none;
        opacity: 0.7;
    }

    .toggle-spinner {
        width: 12px;
        height: 12px;
        border: 2px solid var(--color-text-muted);
        border-top-color: var(--color-accent);
        border-radius: 50%;
        animation: spin 0.8s linear infinite;
    }

    .flip-spinner {
        width: 10px;
        height: 10px;
        box-sizing: border-box;
        border: 2px solid rgba(255, 255, 255, 0.3);
        border-top-color: rgba(255, 255, 255, 0.8);
        border-radius: 50%;
        animation: spin 0.8s linear infinite;
    }

    /* Disabled */
    .toggle-container input:disabled ~ .toggle-slider,
    .toggle-container input:disabled + .flip-track {
        opacity: 0.5;
        cursor: not-allowed;
    }

    /* Group (when label is present) */
    .toggle-group {
        display: flex;
        align-items: center;
        gap: 10px;
    }

    .toggle-text {
        display: flex;
        flex-direction: column;
    }

    .toggle-label {
        font-size: 14px;
        font-weight: 500;
        color: var(--color-text-primary);
        /* Подпись — <label for>, а глобальный стиль форм даёт label отступ
           снизу: гасим, как и у .toggle-container, иначе строка съезжает. */
        margin-bottom: 0;
    }

    .toggle-hint {
        text-wrap: pretty;
        font-size: 12px;
        color: var(--color-text-muted);
        line-height: 1.5;
        margin-top: 2px;
    }

    @keyframes spin {
        to {
            transform: rotate(360deg);
        }
    }
</style>
