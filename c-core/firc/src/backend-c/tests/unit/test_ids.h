#ifndef FIRC_TEST_IDS_H
#define FIRC_TEST_IDS_H

#include <stdlib.h>

#include "firc/id.h"

static inline firc_id_t test_random_id(void) {
    firc_id_t id;
    if (firc_id_random(&id) != FIRC_OK) { abort(); }
    return id;
}

#endif
