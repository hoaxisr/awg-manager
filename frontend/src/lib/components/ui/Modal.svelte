<script lang="ts">
    import type { Snippet } from 'svelte';
    import ConfirmModal from './ConfirmModal.svelte';

    interface Props {
        open: boolean;
        title: string;
        size?: 'sm' | 'md' | 'lg' | 'xl';
        onclose: () => void;
        children: Snippet;
        actions?: Snippet;
        /**
         * Close the modal when the user clicks the dimmed backdrop.
         * Disable for forms with unsaved input where an accidental
         * outside click would lose state — pair with an explicit
         * cancel button + Esc handling (Esc stays enabled regardless).
         */
        closeOnBackdrop?: boolean;
        /**
         * Returns true if the embedded form has unsaved user input. When provided
         * and returns true, the backdrop click, Esc keypress, and the close-X button
         * show a ConfirmModal before invoking onclose. The explicit footer Cancel
         * button is sacred — it bypasses this and calls onclose directly, treating
         * the click as the user's deliberate discard gesture.
         */
        hasUnsavedChanges?: () => boolean;
    }

    let {
        open = $bindable(false),
        title,
        size = 'md',
        onclose,
        children,
        actions,
        closeOnBackdrop = true,
        hasUnsavedChanges,
    }: Props = $props();

    const sizeClasses = {
        sm: 'max-w-sm',
        md: 'max-w-md',
        lg: 'max-w-lg',
        xl: 'max-w-xl'
    };

    function attemptClose() {
        let dirty = false;
        try {
            dirty = hasUnsavedChanges?.() === true;
        } catch {
            // Treat thrown check as clean — better to let the user out than trap
            // them in a modal whose dirty detector is broken.
            dirty = false;
        }
        if (dirty) {
            confirmOpen = true;
        } else {
            onclose();
        }
    }

    function handleKeydown(e: KeyboardEvent) {
        if (e.key === 'Escape') {
            if (confirmOpen) return; // ConfirmModal owns Esc while open
            attemptClose();
        }
    }

    // Tracks whether the current pointer gesture started on the backdrop.
    // Without this, a drag that begins inside .modal-card (text selection,
    // slider, etc.) and releases on the dimmed area would close the modal
    // — a classic accidental-dismiss bug. We require BOTH pointerdown and
    // click to land on the backdrop element itself.
    let backdropEl: HTMLElement | null = $state(null);
    let pointerDownOnBackdrop = false;
    let confirmOpen = $state(false);

    $effect(() => {
        if (!open) confirmOpen = false;
    });

    function handleBackdropPointerDown(e: PointerEvent) {
        pointerDownOnBackdrop = e.target === backdropEl;
    }

    function handleBackdropClick(e: MouseEvent) {
        if (!closeOnBackdrop) return;
        if (confirmOpen) return; // ConfirmModal owns dismissal while open
        if (e.target !== backdropEl) return;
        if (!pointerDownOnBackdrop) return;
        pointerDownOnBackdrop = false;
        attemptClose();
    }

    function handleBackdropKeydown(e: KeyboardEvent) {
        if (!closeOnBackdrop) return;
        if (confirmOpen) return;
        if (e.target !== backdropEl) return;
        if (e.key !== 'Enter' && e.key !== ' ') return;
        e.preventDefault();
        attemptClose();
    }

    // Portal action: moves the backdrop to <body> so it escapes any
    // ancestor stacking context (e.g. position: sticky, transform, filter).
    // Without this, an ancestor with z-index: auto becomes the cap of our
    // z-index: 200 backdrop, letting later siblings paint on top of it.
    function portal(node: HTMLElement) {
        document.body.appendChild(node);
        return {
            destroy() {
                if (node.parentNode) node.parentNode.removeChild(node);
            },
        };
    }
</script>

<svelte:window onkeydown={handleKeydown} />

{#if open}
    <!-- svelte-ignore a11y_no_noninteractive_element_interactions -->
    <!-- svelte-ignore a11y_click_events_have_key_events -->
    <div
        bind:this={backdropEl}
        use:portal
        class="modal-backdrop"
        role="dialog"
        aria-modal="true"
        aria-labelledby="modal-title"
        tabindex="-1"
        onpointerdown={handleBackdropPointerDown}
        onclick={handleBackdropClick}
        onkeydown={handleBackdropKeydown}
    >
        <div
            class="modal-card {sizeClasses[size]}"
            role="document"
        >
            <header class="modal-header">
                <h3 id="modal-title">{title}</h3>
                <button
                    class="modal-close"
                    onclick={attemptClose}
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
    {#if hasUnsavedChanges}
        <ConfirmModal
            open={confirmOpen}
            title="Закрыть без сохранения?"
            message="Все правки будут потеряны."
            confirmLabel="Закрыть"
            cancelLabel="Остаться"
            variant="danger"
            onConfirm={() => { confirmOpen = false; onclose(); }}
            onClose={() => { confirmOpen = false; }}
        />
    {/if}
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
        cursor: pointer;
    }

    .modal-card {
        background: var(--bg-secondary);
        border: 1px solid var(--border);
        border-radius: var(--radius);
        width: 100%;
        cursor: auto;
        /* min-width: 0 + box-sizing keeps the card from being inflated
           past its size-class max-width by an intrinsic min-content child
           (long URL placeholder, monospace text without break-points). */
        min-width: 0;
        box-sizing: border-box;
        /* 100vh on mobile includes hidden browser chrome (address bar,
           toolbar) so the card overflows the visible area. dvh (dynamic
           viewport height) tracks the actual visible space. Fallback to
           vh for older browsers that don't support dvh. */
        max-height: calc(100vh - 2rem);
        max-height: calc(100dvh - 2rem);
        display: flex;
        flex-direction: column;
    }

    /* Each size class caps at its target width but never exceeds the
       visible viewport (minus backdrop padding). */
    .max-w-sm { max-width: min(24rem, calc(100vw - 2rem)); }
    .max-w-md { max-width: min(32rem, calc(100vw - 2rem)); }
    .max-w-lg { max-width: min(40rem, calc(100vw - 2rem)); }
    .max-w-xl { max-width: min(48rem, calc(100vw - 2rem)); }

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

    .modal-close {
        display: flex;
        align-items: center;
        justify-content: center;
        padding: 0.375rem;
        border: none;
        border-radius: var(--radius-sm);
        background: transparent;
        color: var(--text-secondary);
        cursor: pointer;
        flex-shrink: 0;
        transition: color 0.15s ease, background 0.15s ease;
    }

    .modal-close:hover {
        color: var(--text-primary);
        background: var(--bg-hover);
    }

    .modal-close svg {
        width: 1.25rem;
        height: 1.25rem;
    }

    .modal-body {
        padding: 1rem;
        overflow-y: auto;
        overflow-x: hidden;
        flex: 1;
        min-height: 0;
        min-width: 0;
    }

    /* Defensive: ensure form controls inside any modal never push the body
       wider than the card. Inputs/textareas/selects with width:100% +
       box-sizing:border-box should already fit, but min-width:auto on grid
       items can leak intrinsic widths through. */
    .modal-body :global(input),
    .modal-body :global(textarea),
    .modal-body :global(select) {
        max-width: 100%;
        min-width: 0;
        box-sizing: border-box;
    }

    .modal-footer {
        display: flex;
        justify-content: flex-end;
        gap: 0.5rem;
        padding: 1rem;
        border-top: 1px solid var(--border);
    }
</style>
