/* awgm-warpscout: tiny JSON DOM parser (enough for the stdin config). */
#ifndef WS_JSON_H
#define WS_JSON_H

#include <stddef.h>

typedef enum { J_NULL, J_BOOL, J_NUM, J_STR, J_ARR, J_OBJ } jtype;

typedef struct jval {
    jtype t;
    int b;
    double num;
    char *str;           /* J_STR (NUL-terminated, unescaped) */
    struct jval *items;  /* J_ARR / J_OBJ values */
    char **keys;         /* J_OBJ keys */
    int n;
} jval;

/* Returns 0 on success; on error fills err (>= 96 bytes). */
int json_parse(const char *text, size_t len, jval *out, char *err, size_t errlen);
void json_free(jval *v);
const jval *json_get(const jval *obj, const char *key);

#endif
