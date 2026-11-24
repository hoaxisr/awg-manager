#ifndef FIRC_SUB_SOCK_H
#define FIRC_SUB_SOCK_H

#include <stdbool.h>
#include <stdint.h>

#include <curl/curl.h>

typedef struct firc_sub_sock {
    uint32_t mark;
    bool mark_refused;
} firc_sub_sock_t;

int firc_sub_sock_cb(void *ud, curl_socket_t fd, curlsocktype purpose);

#endif
