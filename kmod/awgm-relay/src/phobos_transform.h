/* SPDX-License-Identifier: GPL-2.0 */
/* Трансформация Phobos (спека §3.5, §3.7) поверх примитивов phobos.h. */
#ifndef AWGMR_PHOBOS_TRANSFORM_H
#define AWGMR_PHOBOS_TRANSFORM_H

#include "phobos.h"
#include "transform.h"

struct awgmr_phobos_priv {
	struct awgmr_phobos_cfg cfg;
	struct awgmr_rtp_state rtp;     /* MEDIA; меняет только c2s */
	bool have_key;
	bool have_mask;
	bool hs_done;                   /* был type 2 в любом направлении */
};

extern const struct awgmr_transform awgmr_phobos_transform;

#endif
