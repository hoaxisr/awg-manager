#include "firc/json.h"

#include <stdio.h>
#include <stdlib.h>
#include <string.h>

#include "firc/models.h"

cJSON *firc_json_error(const char *msg) {
    cJSON *obj = cJSON_CreateObject();
    if (!obj) { return NULL; }
    if (!cJSON_AddStringToObject(obj, "error", msg)) {
        cJSON_Delete(obj);
        return NULL;
    }
    return obj;
}

bool firc_json_whole(const cJSON *j, double max, uint64_t *out) {
    if (!cJSON_IsNumber(j)) { return false; }
    double d = j->valuedouble;
    if (!(d >= 0.0 && d <= max)) { return false; }
    uint64_t u = (uint64_t)d;
    if ((double)u != d) { return false; }
    *out = u;
    return true;
}

char *firc_json_dump(const cJSON *obj) {
    return cJSON_PrintUnformatted(obj);
}

static cJSON *strings_to_json_array(char *const *items, size_t n) {
    cJSON *arr = cJSON_CreateArray();
    for (size_t i = 0; arr != NULL && i < n; i++) {
        cJSON *s = cJSON_CreateString(items[i]);
        if (s == NULL || !cJSON_AddItemToArray(arr, s)) {
            cJSON_Delete(s);
            cJSON_Delete(arr);
            return NULL;
        }
    }
    return arr;
}

static firc_err_t json_string_list(const cJSON *arr, char ***out, size_t *out_n) {
    *out = NULL;
    *out_n = 0;
    if (arr == NULL || cJSON_IsNull(arr)) { return FIRC_OK; }
    if (!cJSON_IsArray(arr)) { return FIRC_ERR_INVAL; }
    size_t n = (size_t)cJSON_GetArraySize(arr);
    if (n == 0) { return FIRC_OK; }
    char **list = calloc(n, sizeof(*list));
    if (list == NULL) { return FIRC_ERR_NOMEM; }
    size_t i = 0;
    const cJSON *it = NULL;
    cJSON_ArrayForEach(it, arr) {
        if (!cJSON_IsString(it)) {
            for (size_t j = 0; j < i; j++) { free(list[j]); }
            free(list);
            return FIRC_ERR_INVAL;
        }
        list[i] = strdup(it->valuestring);
        if (list[i] == NULL) {
            for (size_t j = 0; j < i; j++) { free(list[j]); }
            free(list);
            return FIRC_ERR_NOMEM;
        }
        i++;
    }
    *out = list;
    *out_n = n;
    return FIRC_OK;
}

firc_err_t firc_api_devices_from_req(const cJSON *req, const firc_devsel_spec_t *existing,
                                     firc_devsel_spec_t *out, const char **err_msg, const char **err_field,
                                     char *msgbuf, size_t msgbuf_len) {
    const cJSON *dev_j = cJSON_GetObjectItemCaseSensitive(req, "devices");
    if (dev_j == NULL) {
        return existing != NULL ? firc_devsel_spec_copy(out, existing) : FIRC_OK;
    }
    if (cJSON_IsNull(dev_j)) { return FIRC_OK; }
    if (!cJSON_IsObject(dev_j)) {
        *err_msg = "devices must be an object";
        *err_field = "devices";
        return FIRC_ERR_INVAL;
    }
    firc_err_t err = json_string_list(cJSON_GetObjectItemCaseSensitive(dev_j, "allow"), &out->allow,
                                      &out->n_allow);
    if (err == FIRC_OK) {
        err = json_string_list(cJSON_GetObjectItemCaseSensitive(dev_j, "deny"), &out->deny,
                               &out->n_deny);
    }
    if (err == FIRC_ERR_INVAL) {
        *err_msg = "devices: allow and deny are lists of strings";
        *err_field = "devices";
    }
    bool in_allow;
    const char *bad_entry = NULL, *why = NULL;
    if (err == FIRC_OK && firc_devsel_spec_check_canon(out, &in_allow, &bad_entry, &why) == FIRC_ERR_INVAL) {
        snprintf(msgbuf, msgbuf_len, "devices.%s \"%.64s\" %s", in_allow ? "allow" : "deny",
                bad_entry != NULL ? bad_entry : "", why);
        *err_msg = msgbuf;
        *err_field = in_allow ? "devices.allow" : "devices.deny";
        err = FIRC_ERR_INVAL;
    }
    if (err != FIRC_OK) { firc_devsel_spec_clear(out); }
    return err;
}

cJSON *firc_api_devices_to_json(const firc_devsel_spec_t *ds) {
    cJSON *obj = cJSON_CreateObject();
    cJSON *allow = strings_to_json_array(ds->allow, ds->n_allow);
    cJSON *deny = strings_to_json_array(ds->deny, ds->n_deny);
    if (obj == NULL || allow == NULL || deny == NULL) {
        cJSON_Delete(obj);
        cJSON_Delete(allow);
        cJSON_Delete(deny);
        return NULL;
    }
    cJSON_AddItemToObject(obj, "allow", allow);
    cJSON_AddItemToObject(obj, "deny", deny);
    return obj;
}

cJSON *firc_api_list_progress_json(const firc_group_list_t *l) {
    cJSON *obj = cJSON_CreateObject();
    if (obj == NULL) { return NULL; }
    if (l->sync_progress.applying) {
        cJSON_AddStringToObject(obj, "stage", "apply");
    } else if (l->sync_progress.stage == FIRC_SUB_STAGE_PARSE) {
        cJSON_AddStringToObject(obj, "stage", "parse");
        cJSON_AddNumberToObject(obj, "lines", (double)l->sync_progress.lines);
    } else {
        cJSON_AddStringToObject(obj, "stage", "fetch");
        cJSON_AddNumberToObject(obj, "bytes", (double)l->sync_progress.bytes);
        cJSON_AddNumberToObject(obj, "total", (double)l->sync_progress.total); /* 0: server declared no length */
    }
    return obj;
}

cJSON *firc_api_list_sync_json(const firc_group_list_t *l) {
    cJSON *obj = cJSON_CreateObject();
    if (obj == NULL) { return NULL; }
    if (cJSON_AddStringToObject(obj, "state", firc_sub_sync_state_name(l->sync_state)) == NULL ||
        cJSON_AddStringToObject(obj, "error", l->sync_error) == NULL ||
        cJSON_AddNumberToObject(obj, "lastCheck", l->last_check) == NULL) {
        cJSON_Delete(obj);
        return NULL;
    }
    return obj;
}
