#include "firc/queue.h"

#include <errno.h>
#include <pthread.h>
#include <stdbool.h>
#include <stdlib.h>
#include <time.h>

struct firc_queue {
    pthread_mutex_t mu;
    pthread_cond_t nonempty;
    void **items;
    size_t capacity;
    size_t head;
    size_t len;
    uint64_t dropped;
    firc_queue_policy_t policy;
    bool closed;
};

firc_queue_t *firc_queue_create(size_t capacity, firc_queue_policy_t policy)
{
    if (capacity == 0) {
        return NULL;
    }
    firc_queue_t *q = calloc(1, sizeof(*q));
    if (q == NULL) {
        return NULL;
    }
    q->items = (void **)calloc(capacity, sizeof(void *));
    if (q->items == NULL) {
        free(q);
        return NULL;
    }
    q->capacity = capacity;
    q->policy = policy;
    if (pthread_mutex_init(&q->mu, NULL) != 0) {
        free((void *)q->items);
        free(q);
        return NULL;
    }
    pthread_condattr_t attr;
    if (pthread_condattr_init(&attr) != 0 ||
        pthread_condattr_setclock(&attr, CLOCK_MONOTONIC) != 0 ||
        pthread_cond_init(&q->nonempty, &attr) != 0) {
        pthread_mutex_destroy(&q->mu);
        free((void *)q->items);
        free(q);
        return NULL;
    }
    pthread_condattr_destroy(&attr);
    return q;
}

void firc_queue_destroy(firc_queue_t *q)
{
    if (q == NULL) {
        return;
    }
    pthread_cond_destroy(&q->nonempty);
    pthread_mutex_destroy(&q->mu);
    free((void *)q->items);
    free(q);
}

firc_err_t firc_queue_push(firc_queue_t *q, void *item, void **evicted)
{
    firc_err_t err = FIRC_OK;
    pthread_mutex_lock(&q->mu);
    if (q->closed) {
        err = FIRC_ERR_CLOSED;
    } else if (q->len == q->capacity) {
        if (q->policy == FIRC_QUEUE_REJECT) {
            q->dropped++;
            err = FIRC_ERR_LIMIT;
        } else {
            if (evicted != NULL) {
                *evicted = q->items[q->head];
            }
            q->head = (q->head + 1) % q->capacity;
            q->len--;
            q->dropped++;
            q->items[(q->head + q->len) % q->capacity] = item;
            q->len++;
        }
    } else {
        q->items[(q->head + q->len) % q->capacity] = item;
        q->len++;
    }
    if (err == FIRC_OK) {
        pthread_cond_signal(&q->nonempty);
    }
    pthread_mutex_unlock(&q->mu);
    return err;
}

static firc_err_t pop_locked(firc_queue_t *q, void **item)
{
    if (q->len == 0) {
        return q->closed ? FIRC_ERR_CLOSED : FIRC_ERR_AGAIN;
    }
    *item = q->items[q->head];
    q->head = (q->head + 1) % q->capacity;
    q->len--;
    return FIRC_OK;
}

firc_err_t firc_queue_try_pop(firc_queue_t *q, void **item)
{
    pthread_mutex_lock(&q->mu);
    firc_err_t err = pop_locked(q, item);
    pthread_mutex_unlock(&q->mu);
    return err;
}

firc_err_t firc_queue_pop(firc_queue_t *q, void **item, int timeout_ms)
{
    if (timeout_ms == 0) {
        return firc_queue_try_pop(q, item);
    }

    struct timespec deadline;
    if (timeout_ms > 0) {
        clock_gettime(CLOCK_MONOTONIC, &deadline);
        deadline.tv_sec += timeout_ms / 1000;
        deadline.tv_nsec += (long)(timeout_ms % 1000) * 1000000L;
        if (deadline.tv_nsec >= 1000000000L) {
            deadline.tv_sec += 1;
            deadline.tv_nsec -= 1000000000L;
        }
    }

    pthread_mutex_lock(&q->mu);
    for (;;) {
        firc_err_t err = pop_locked(q, item);
        if (err != FIRC_ERR_AGAIN) {
            pthread_mutex_unlock(&q->mu);
            return err;
        }
        int rc;
        if (timeout_ms < 0) {
            rc = pthread_cond_wait(&q->nonempty, &q->mu);
        } else {
            rc = pthread_cond_timedwait(&q->nonempty, &q->mu, &deadline);
        }
        if (rc == ETIMEDOUT) {
            pthread_mutex_unlock(&q->mu);
            return FIRC_ERR_TIMEOUT;
        }
    }
}

void firc_queue_close(firc_queue_t *q)
{
    pthread_mutex_lock(&q->mu);
    q->closed = true;
    pthread_cond_broadcast(&q->nonempty);
    pthread_mutex_unlock(&q->mu);
}

size_t firc_queue_len(firc_queue_t *q)
{
    pthread_mutex_lock(&q->mu);
    size_t n = q->len;
    pthread_mutex_unlock(&q->mu);
    return n;
}

size_t firc_queue_capacity(const firc_queue_t *q)
{
    return q->capacity;
}

uint64_t firc_queue_dropped(firc_queue_t *q)
{
    pthread_mutex_lock(&q->mu);
    uint64_t n = q->dropped;
    pthread_mutex_unlock(&q->mu);
    return n;
}
