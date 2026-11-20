#ifndef FIRC_JSON_H
#define FIRC_JSON_H

#include <cjson/cJSON.h>
#include <stdbool.h>
#include <stdint.h>

#include "firc/err.h"

typedef struct firc_devsel_spec firc_devsel_spec_t; /* full definition in firc/models.h */
typedef struct firc_group_list firc_group_list_t; /* full definition in firc/models.h */

/* {"error": "<msg>"}; caller owns the result (cJSON_Delete) */
cJSON *firc_json_error(const char *msg);

/* true and *out set when j is a whole number in 0..max, checked on the double before any cast */
bool firc_json_whole(const cJSON *j, double max, uint64_t *out);

/* compact serialization; caller frees with free(); NULL on OOM */
char *firc_json_dump(const cJSON *obj);

/* absent keeps existing (NULL: none), null is every device; FIRC_ERR_INVAL sets *err_field and *err_msg */
firc_err_t firc_api_devices_from_req(const cJSON *req, const firc_devsel_spec_t *existing,
                                     firc_devsel_spec_t *out, const char **err_msg, const char **err_field,
                                     char *msgbuf, size_t msgbuf_len);

/* {"allow": [...], "deny": [...]}, both keys always present; NULL on OOM */
cJSON *firc_api_devices_to_json(const firc_devsel_spec_t *ds);

/* one `progress` payload, named by the stage last heard about: fetch/parse/apply; NULL on OOM */
cJSON *firc_api_list_progress_json(const firc_group_list_t *l);

/* {"state","error","lastCheck"}, the sync half of a list's record; NULL on OOM */
cJSON *firc_api_list_sync_json(const firc_group_list_t *l);

#endif /* FIRC_JSON_H */
