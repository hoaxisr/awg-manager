#include "firc/id.h"

#include <stdio.h>
#include <stdlib.h>
#include <string.h>

#include "firc/rand.h"

static int hex_val(char c)
{
    if (c >= '0' && c <= '9') {
        return c - '0';
    }
    if (c >= 'a' && c <= 'f') {
        return c - 'a' + 10;
    }
    if (c >= 'A' && c <= 'F') {
        return c - 'A' + 10;
    }
    return -1;
}

firc_err_t firc_id_parse(const char *s, firc_id_t *out)
{
    if (s == NULL || strlen(s) != 8) {
        return FIRC_ERR_INVAL;
    }
    firc_id_t id;
    for (int i = 0; i < 4; i++) {
        int hi = hex_val(s[(size_t)i * 2]);
        int lo = hex_val(s[(size_t)i * 2 + 1]);
        if (hi < 0 || lo < 0) {
            return FIRC_ERR_INVAL;
        }
        id.b[i] = (uint8_t)((hi << 4) | lo);
    }
    *out = id;
    return FIRC_OK;
}

void firc_id_format(firc_id_t id, char *buf)
{
    static const char digits[] = "0123456789abcdef";
    for (int i = 0; i < 4; i++) {
        buf[(size_t)i * 2] = digits[id.b[i] >> 4];
        buf[(size_t)i * 2 + 1] = digits[id.b[i] & 0x0f];
    }
    buf[8] = '\0';
}

bool firc_id_is_zero(firc_id_t id)
{
    return id.b[0] == 0 && id.b[1] == 0 && id.b[2] == 0 && id.b[3] == 0;
}

bool firc_id_equal(firc_id_t a, firc_id_t b)
{
    return memcmp(a.b, b.b, 4) == 0;
}

firc_err_t firc_id_random(firc_id_t *out)
{
    firc_id_t id = {{0, 0, 0, 0}};
    while (firc_id_is_zero(id)) {
        firc_err_t err = firc_random_bytes(id.b, sizeof(id.b));
        if (err != FIRC_OK) { return err; }
    }
    *out = id;
    return FIRC_OK;
}

firc_err_t firc_id_pool_next(firc_id_pool_t *pool, firc_id_t *out)
{
    for (;;) {
        if (pool->at + sizeof(out->b) > pool->len) {
            firc_err_t err = firc_random_bytes(pool->bytes, sizeof(pool->bytes));
            if (err != FIRC_OK) { return err; }
            pool->at = 0;
            pool->len = sizeof(pool->bytes);
        }
        firc_id_t id;
        memcpy(id.b, pool->bytes + pool->at, sizeof(id.b));
        pool->at += sizeof(id.b);
        if (!firc_id_is_zero(id)) {
            *out = id;
            return FIRC_OK;
        }
    }
}

static uint32_t id_word(firc_id_t id)
{
    return ((uint32_t)id.b[0] << 24) | ((uint32_t)id.b[1] << 16) | ((uint32_t)id.b[2] << 8) |
           (uint32_t)id.b[3];
}

/* full 32-bit finalizer: a cheap multiply-shift hash collapses badly past 65536 ids */
static size_t id_slot(const firc_id_set_t *set, uint32_t w)
{
    uint32_t h = w;
    h ^= h >> 16;
    h *= 0x7feb352du;
    h ^= h >> 15;
    h *= 0x846ca68bu;
    h ^= h >> 16;
    size_t at = (size_t)h & (set->n_slots - 1);
    while (set->slots[at] != 0 && set->slots[at] != w) {
        at = (at + 1) & (set->n_slots - 1);
    }
    return at;
}

bool firc_id_set_contains(const firc_id_set_t *set, firc_id_t id)
{
    if (set->n_slots == 0) { return false; }
    uint32_t w = id_word(id);
    if (w == 0) { return false; }
    return set->slots[id_slot(set, w)] == w;
}

firc_err_t firc_id_set_add(firc_id_set_t *set, firc_id_t id)
{
    uint32_t w = id_word(id);
    if (w == 0) { return FIRC_OK; } /* the zero id is never stored */
    if (set->n_slots == 0 || set->len * 2 >= set->n_slots) {
        size_t want = set->n_slots == 0 ? 128 : set->n_slots * 2;
        uint32_t *fresh = calloc(want, sizeof(*fresh));
        if (fresh == NULL) { return FIRC_ERR_NOMEM; }
        firc_id_set_t grown = {fresh, want, 0};
        for (size_t i = 0; i < set->n_slots; i++) {
            if (set->slots[i] != 0) {
                fresh[id_slot(&grown, set->slots[i])] = set->slots[i];
                grown.len++;
            }
        }
        free(set->slots);
        *set = grown;
    }
    size_t at = id_slot(set, w);
    if (set->slots[at] == 0) {
        set->slots[at] = w;
        set->len++;
    }
    return FIRC_OK;
}

void firc_id_set_free(firc_id_set_t *set)
{
    free(set->slots);
    set->slots = NULL;
    set->n_slots = 0;
    set->len = 0;
}
