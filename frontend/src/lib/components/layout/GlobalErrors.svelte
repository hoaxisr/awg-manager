<script lang="ts">
    import { errorStore } from '$lib/stores/errors';
    import { fly } from 'svelte/transition';

    const activeErrors = errorStore.active;
</script>

{#if $activeErrors.length > 0}
    <div class="error-container">
        {#each $activeErrors as error (error.id)}
            <div
                class="error-toast"
                transition:fly={{ x: 100, duration: 200 }}
            >
                <div class="error-content">
                    <svg xmlns="http://www.w3.org/2000/svg" class="error-icon" viewBox="0 0 20 20" fill="currentColor">
                        <path fill-rule="evenodd" d="M18 10a8 8 0 11-16 0 8 8 0 0116 0zm-7 4a1 1 0 11-2 0 1 1 0 012 0zm-1-9a1 1 0 00-1 1v4a1 1 0 102 0V6a1 1 0 00-1-1z" clip-rule="evenodd" />
                    </svg>
                    <div class="error-text">
                        {#if error.context}
                            <p class="error-context">{error.context}</p>
                        {/if}
                        <p class="error-message">{error.message}</p>
                    </div>
                    <button
                        class="btn btn-icon"
                        onclick={() => errorStore.dismiss(error.id)}
                        aria-label="Dismiss error"
                    >
                        <svg xmlns="http://www.w3.org/2000/svg" viewBox="0 0 20 20" fill="currentColor">
                            <path fill-rule="evenodd" d="M4.293 4.293a1 1 0 011.414 0L10 8.586l4.293-4.293a1 1 0 111.414 1.414L11.414 10l4.293 4.293a1 1 0 01-1.414 1.414L10 11.414l-4.293 4.293a1 1 0 01-1.414-1.414L8.586 10 4.293 5.707a1 1 0 010-1.414z" clip-rule="evenodd" />
                        </svg>
                    </button>
                </div>
            </div>
        {/each}
    </div>
{/if}

<style>
    .error-container {
        position: fixed;
        bottom: 1rem;
        right: 1rem;
        z-index: 50;
        display: flex;
        flex-direction: column;
        gap: 0.5rem;
        max-width: 24rem;
    }

    .error-toast {
        background: var(--bg-tertiary);
        border: 1px solid var(--error);
        border-left: 3px solid var(--error);
        border-radius: var(--radius);
        padding: 1rem;
        box-shadow: var(--shadow);
    }

    .error-content {
        display: flex;
        align-items: flex-start;
        gap: 0.75rem;
    }

    .error-icon {
        width: 1.25rem;
        height: 1.25rem;
        flex-shrink: 0;
        margin-top: 0.125rem;
        color: var(--error);
    }

    .error-text {
        flex: 1;
        min-width: 0;
    }

    .error-context {
        font-size: 0.75rem;
        opacity: 0.8;
        margin-bottom: 0.25rem;
    }

    .error-message {
        font-size: 0.875rem;
        font-weight: 500;
    }

    .error-toast .btn-icon svg {
        width: 1rem;
        height: 1rem;
    }
</style>
