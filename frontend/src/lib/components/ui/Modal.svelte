<script lang="ts">
    import type { Snippet } from 'svelte';

    interface Props {
        open: boolean;
        title: string;
        size?: 'sm' | 'md' | 'lg' | 'xl';
        onclose: () => void;
        children: Snippet;
        actions?: Snippet;
    }

    let {
        open = $bindable(false),
        title,
        size = 'md',
        onclose,
        children,
        actions
    }: Props = $props();

    const sizeClasses = {
        sm: 'max-w-sm',
        md: 'max-w-md',
        lg: 'max-w-lg',
        xl: 'max-w-xl'
    };

    function handleKeydown(e: KeyboardEvent) {
        if (e.key === 'Escape') {
            onclose();
        }
    }
</script>

<svelte:window onkeydown={handleKeydown} />

{#if open}
    <!-- svelte-ignore a11y_no_noninteractive_element_interactions -->
    <div
        class="modal-backdrop"
        onclick={onclose}
        onkeydown={(e) => e.key === 'Enter' && onclose()}
        role="dialog"
        aria-modal="true"
        aria-labelledby="modal-title"
        tabindex="-1"
    >
        <!-- svelte-ignore a11y_no_noninteractive_element_interactions -->
        <div
            class="modal-card {sizeClasses[size]}"
            onclick={(e) => e.stopPropagation()}
            onkeydown={(e) => e.stopPropagation()}
            role="document"
        >
            <header class="modal-header">
                <h3 id="modal-title">{title}</h3>
                <button
                    class="btn btn-icon"
                    onclick={onclose}
                    aria-label="Close modal"
                >
                    <svg xmlns="http://www.w3.org/2000/svg" viewBox="0 0 20 20" fill="currentColor">
                        <path fill-rule="evenodd" d="M4.293 4.293a1 1 0 011.414 0L10 8.586l4.293-4.293a1 1 0 111.414 1.414L11.414 10l4.293 4.293a1 1 0 01-1.414 1.414L10 11.414l-4.293 4.293a1 1 0 01-1.414-1.414L8.586 10 4.293 5.707a1 1 0 010-1.414z" clip-rule="evenodd" />
                    </svg>
                </button>
            </header>

            <section class="modal-body">
                {@render children()}
            </section>

            {#if actions}
                <footer class="modal-footer">
                    {@render actions()}
                </footer>
            {/if}
        </div>
    </div>
{/if}

<style>
    .modal-backdrop {
        position: fixed;
        inset: 0;
        z-index: 200;
        display: flex;
        align-items: center;
        justify-content: center;
        padding: 1rem;
        background: rgba(0, 0, 0, 0.5);
        overflow-y: auto;
    }

    .modal-card {
        background: var(--bg-secondary);
        border: 1px solid var(--border);
        border-radius: var(--radius, 8px);
        width: 100%;
        max-height: calc(100vh - 2rem);
        display: flex;
        flex-direction: column;
    }

    .max-w-sm { max-width: 24rem; }
    .max-w-md { max-width: 32rem; }
    .max-w-lg { max-width: 40rem; }
    .max-w-xl { max-width: 48rem; }

    .modal-header {
        display: flex;
        align-items: center;
        justify-content: space-between;
        padding: 1rem;
        border-bottom: 1px solid var(--border);
    }

    .modal-header h3 {
        font-size: 1.125rem;
        font-weight: 600;
    }

    .modal-header .btn-icon svg {
        width: 1.25rem;
        height: 1.25rem;
    }

    .modal-body {
        padding: 1rem;
        overflow-y: auto;
        flex: 1;
        min-height: 0;
    }

    .modal-footer {
        display: flex;
        justify-content: flex-end;
        gap: 0.5rem;
        padding: 1rem;
        border-top: 1px solid var(--border);
    }
</style>
