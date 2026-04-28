<script lang="ts">
    interface Props {
        checked: boolean;
        onchange?: (checked: boolean) => void;
        disabled?: boolean;
        label?: string;
        hint?: string;
        size?: 'sm' | 'md';
    }

    let {
        checked = $bindable(),
        onchange,
        disabled = false,
        label = '',
        hint = '',
        size = 'md',
    }: Props = $props();

    function handleChange() {
        if (onchange) onchange(checked);
    }
</script>

{#if label}
    <div class="toggle-group">
        <label class="toggle-container" class:sm={size === 'sm'}>
            <input type="checkbox" bind:checked {disabled} onchange={handleChange} />
            <span class="toggle-slider"></span>
        </label>
        <div class="toggle-text">
            <span class="toggle-label">{label}</span>
            {#if hint}
                <span class="toggle-hint">{hint}</span>
            {/if}
        </div>
    </div>
{:else}
    <label class="toggle-container" class:sm={size === 'sm'}>
        <input type="checkbox" bind:checked {disabled} onchange={handleChange} />
        <span class="toggle-slider"></span>
    </label>
{/if}

<style>
    .toggle-container {
        position: relative;
        display: inline-flex;
        align-items: center;
        cursor: pointer;
    }

    .toggle-container input {
        position: absolute;
        opacity: 0;
        width: 0;
        height: 0;
    }

    .toggle-slider {
        position: relative;
        width: 44px;
        height: 24px;
        background: var(--bg-tertiary);
        border-radius: var(--radius-pill);
        transition: background 0.2s ease;
    }

    .toggle-slider::before {
        content: '';
        position: absolute;
        top: 2px;
        left: 2px;
        width: 20px;
        height: 20px;
        background: var(--text-muted);
        border-radius: 50%;
        transition: transform 0.2s ease, background 0.2s ease;
    }

    .toggle-container input:checked + .toggle-slider {
        background: var(--accent);
    }

    .toggle-container input:checked + .toggle-slider::before {
        transform: translateX(20px);
        background: white;
    }

    .toggle-container:hover .toggle-slider {
        background: var(--border);
    }

    .toggle-container input:checked:hover + .toggle-slider {
        filter: brightness(1.1);
    }

    .toggle-container.sm .toggle-slider {
        width: 32px;
        height: 18px;
        border-radius: var(--radius-pill);
    }

    .toggle-container.sm .toggle-slider::before {
        width: 14px;
        height: 14px;
        top: 2px;
        left: 2px;
    }

    .toggle-container.sm input:checked + .toggle-slider::before {
        transform: translateX(14px);
    }

    .toggle-container input:disabled + .toggle-slider {
        opacity: 0.5;
        cursor: not-allowed;
    }

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
        color: var(--text-primary);
    }

    .toggle-hint {
        font-size: 12px;
        color: var(--text-muted);
        line-height: 1.5;
        margin-top: 2px;
    }
</style>
