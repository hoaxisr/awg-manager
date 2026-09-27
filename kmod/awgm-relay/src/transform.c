// SPDX-License-Identifier: GPL-2.0
/* Реестр трансформаций (transform.h). */
#include "transform.h"
#include "phobos_transform.h"

static const struct awgmr_transform *const transforms[] = {
	&awgmr_phobos_transform,
	NULL,
};

void awgmr_transforms_init(void)
{
	int i;

	for (i = 0; transforms[i]; i++)
		if (transforms[i]->global_init)
			transforms[i]->global_init();
}

const struct awgmr_transform *awgmr_transform_find(const char *name)
{
	int i;

	for (i = 0; transforms[i]; i++)
		if (!strcmp(transforms[i]->name, name))
			return transforms[i];
	return NULL;
}
