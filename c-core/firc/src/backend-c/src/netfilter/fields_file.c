#include "firc/fields_file.h"

#include <errno.h>
#include <fcntl.h>
#include <stdio.h>
#include <stdlib.h>
#include <string.h>
#include <unistd.h>

#include "firc/mark.h"

#define FIELDS_HEADER "firc-fields\n"
#define FIELDS_END "end\n"
#define FIELDS_MAX_BYTES 8192

static firc_err_t write_all(int fd, const firc_rtnl_field_t *v, size_t n) {
    FILE *fp = fdopen(fd, "w");
    if (fp == NULL) {
        int e = errno;
        close(fd);
        return firc_err_from_errno(e);
    }
    bool ok = fputs(FIELDS_HEADER, fp) >= 0;
    for (size_t i = 0; ok && i < n; i++) {
        ok = fprintf(fp, "field %s %u\n", v[i].owner, (unsigned)v[i].field) > 0;
    }
    ok = ok && fputs(FIELDS_END, fp) >= 0 && fflush(fp) == 0 && fsync(fileno(fp)) == 0;
    int e = errno;
    if (fclose(fp) != 0 && ok) {
        e = errno;
        ok = false;
    }
    return ok ? FIRC_OK : firc_err_from_errno(e != 0 ? e : EIO);
}

firc_err_t firc_fields_file_save(const char *path, const firc_rtnl_field_t *v, size_t n) {
    if (path == NULL || (v == NULL && n > 0)) { return FIRC_ERR_INVAL; }
    char tmp[512];
    if (snprintf(tmp, sizeof(tmp), "%s.tmp", path) >= (int)sizeof(tmp)) { return FIRC_ERR_INVAL; }
    firc_err_t err = FIRC_OK;
    int fd = open(tmp, O_WRONLY | O_CREAT | O_TRUNC | O_CLOEXEC, 0600);
    if (fd < 0) {
        err = firc_err_from_errno(errno);
    } else {
        err = write_all(fd, v, n);
        if (err == FIRC_OK && rename(tmp, path) != 0) { err = firc_err_from_errno(errno); }
    }
    if (err != FIRC_OK) {
        unlink(tmp);
        unlink(path);
    }
    return err;
}

static bool group_owner(const char *s, size_t n) {
    if (n != 8) { return false; }
    for (size_t i = 0; i < n; i++) {
        if (!((s[i] >= '0' && s[i] <= '9') || (s[i] >= 'a' && s[i] <= 'f'))) { return false; }
    }
    return true;
}

static bool tunnel_owner(const char *s, size_t n) {
    size_t pl = sizeof(FIRC_MARK_TUNNEL_OWNER) - 1;
    if (n <= pl || n >= FIRC_FIELDS_OWNER_LEN || memcmp(s, FIRC_MARK_TUNNEL_OWNER, pl) != 0) { return false; }
    for (size_t i = pl; i < n; i++) {
        char c = s[i];
        if (!((c >= '0' && c <= '9') || (c >= 'a' && c <= 'z') || (c >= 'A' && c <= 'Z') || c == '_' || c == '-')) {
            return false;
        }
    }
    return true;
}

static bool parse_entry(const char *line, size_t len, firc_fields_entry_t *e) {
    static const char prefix[] = "field ";
    size_t pl = sizeof(prefix) - 1;
    if (len < pl + 3 || memcmp(line, prefix, pl) != 0) { return false; }
    const char *id = line + pl;
    const char *sp = memchr(id, ' ', len - pl);
    if (sp == NULL) { return false; }
    size_t il = (size_t)(sp - id);
    if (!group_owner(id, il) && !tunnel_owner(id, il)) { return false; }
    const char *num = sp + 1;
    size_t nl = len - (size_t)(num - line);
    if (nl == 0 || nl > 3 || num[0] == '0') { return false; }
    uint32_t field = 0;
    for (size_t i = 0; i < nl; i++) {
        if (num[i] < '0' || num[i] > '9') { return false; }
        field = field * 10 + (uint32_t)(num[i] - '0');
    }
    if (field < 1 || field > FIRC_MARK_MAX_GROUPS) { return false; }
    memcpy(e->owner, id, il);
    e->owner[il] = '\0';
    e->field = field;
    return true;
}

static firc_err_t parse(const char *buf, size_t len, firc_fields_entry_t *v, size_t *n) {
    size_t hl = sizeof(FIELDS_HEADER) - 1;
    if (len < hl || memcmp(buf, FIELDS_HEADER, hl) != 0) { return FIRC_ERR_INVAL; }
    size_t at = hl;
    *n = 0;
    while (at < len) {
        const char *line = buf + at;
        const char *nlp = memchr(line, '\n', len - at);
        if (nlp == NULL) { return FIRC_ERR_INVAL; }
        size_t ll = (size_t)(nlp - line);
        at += ll + 1;
        if (ll == 3 && memcmp(line, "end", 3) == 0) { return at == len ? FIRC_OK : FIRC_ERR_INVAL; }
        if (*n == FIRC_MARK_MAX_GROUPS) { return FIRC_ERR_INVAL; }
        firc_fields_entry_t e;
        if (!parse_entry(line, ll, &e)) { return FIRC_ERR_INVAL; }
        for (size_t i = 0; i < *n; i++) {
            if (v[i].field == e.field || strcmp(v[i].owner, e.owner) == 0) { return FIRC_ERR_INVAL; }
        }
        v[(*n)++] = e;
    }
    return FIRC_ERR_INVAL;
}

firc_err_t firc_fields_file_load(const char *path, firc_fields_entry_t **out, size_t *n) {
    *out = NULL;
    *n = 0;
    if (path == NULL) { return FIRC_ERR_INVAL; }
    FILE *fp = fopen(path, "r");
    if (fp == NULL) { return errno == ENOENT ? FIRC_ERR_NOENT : firc_err_from_errno(errno); }
    char *buf = malloc(FIELDS_MAX_BYTES + 1);
    firc_fields_entry_t *v = calloc(FIRC_MARK_MAX_GROUPS, sizeof(*v));
    if (buf == NULL || v == NULL) {
        free(buf);
        free(v);
        fclose(fp);
        return FIRC_ERR_NOMEM;
    }
    size_t len = fread(buf, 1, FIELDS_MAX_BYTES + 1, fp);
    bool read_err = ferror(fp) != 0;
    fclose(fp);
    size_t got = 0;
    firc_err_t err = read_err ? FIRC_ERR_IO : len > FIELDS_MAX_BYTES ? FIRC_ERR_INVAL : parse(buf, len, v, &got);
    free(buf);
    if (err != FIRC_OK) {
        free(v);
        return err;
    }
    *out = v;
    *n = got;
    return FIRC_OK;
}
