<script lang="ts">
    interface Props {
        checked: boolean;
        onchange: (checked: boolean) => void;
        loading?: boolean;
        disabled?: boolean;
        label?: string;
        hint?: string;
        size?: 'sm' | 'md';
        variant?: 'slider' | 'flip';
    }

    let {
        checked = $bindable(),
        onchange,
        loading = false,
        disabled = false,
        label = '',
        hint = '',
        size = 'md',
        variant = 'slider',
    }: Props = $props();

    function handleInput(event: Event) {
        if (loading || disabled) {
            event.preventDefault();
            return;
        }
        const input = event.currentTarget as HTMLInputElement;
        const nextChecked = input.checked;
        checked = nextChecked;
        if (onchange) onchange(nextChecked);
    }
</script>

{#if label}
    <div class="toggle-group">
        <label class="toggle-container" class:loading class:sm={size === 'sm'} class:flip={variant === 'flip'}>
            <input type="checkbox" checked={checked} {disabled} oninput={handleInput} />
            {#if variant === 'flip'}
                <span class="flip-track">
                    <span class="flip-lever">
                        {#if loading}
                            <span class="flip-spinner"></span>
                        {/if}
                    </span>
                </span>
            {:else}
                <span class="toggle-spinner-slot" aria-hidden="true">
                    {#if loading}<span class="toggle-spinner"></span>{/if}
                </span>
                <span class="toggle-slider"></span>
            {/if}
        </label>
        <div class="toggle-text">
            <span class="toggle-label">{label}</span>
            {#if hint}
                <span class="toggle-hint">{hint}</span>
            {/if}
        </div>
    </div>
{:else}
    <label class="toggle-container" class:loading class:sm={size === 'sm'} class:flip={variant === 'flip'}>
        <input type="checkbox" checked={checked} {disabled} oninput={handleInput} />
        {#if variant === 'flip'}
            <span class="flip-track">
                <span class="flip-lever">
                    {#if loading}
                        <span class="flip-spinner"></span>
                    {/if}
                </span>
            </span>
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
        border-radius: 50%;
        transition: transform 0.2s ease, background 0.2s ease;
    }

    .toggle-container input:checked ~ .toggle-slider {
        background: var(--color-accent);
    }

    .toggle-container input:checked ~ .toggle-slider::before {
        transform: translateX(20px);
        background: white;
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
        transition: background 0.2s ease, box-shadow 0.2s ease;
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
        transition: transform 0.2s ease, background 0.2s ease, box-shadow 0.2s ease;
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

    /* Hover */
    .toggle-container.flip:hover .flip-lever {
        filter: brightness(1.15);
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
    }

    .toggle-hint {
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
