#ifndef FIRC_ERR_H
#define FIRC_ERR_H

typedef enum firc_err {
    FIRC_OK = 0,
    FIRC_ERR_NOMEM,   /* allocation failure */
    FIRC_ERR_INVAL,   /* invalid argument / malformed input */
    FIRC_ERR_IO,      /* I/O or syscall failure */
    FIRC_ERR_AGAIN,   /* transient: retry later */
    FIRC_ERR_LIMIT,   /* bounded resource exhausted (queue full, cap hit) */
    FIRC_ERR_TIMEOUT, /* deadline expired */
    FIRC_ERR_CLOSED,  /* object already shut down */
    FIRC_ERR_EXIST,   /* duplicate / already present */
    FIRC_ERR_NOENT,   /* not found */
    FIRC_ERR_PROTO,   /* protocol violation */
    FIRC_ERR_STATE,   /* operation invalid in current lifecycle state */
    FIRC_ERR_SYS,     /* unclassified system error */
    FIRC_ERR_UPSTREAM, /* remote fetch failed; maps to one HTTP status (502) */
    FIRC_ERR_NOSYS,   /* kernel/platform does not provide this; same answer next time */
    FIRC_ERR_CANCELED, /* work abandoned on request, not a failure; restart from scratch */
} firc_err_t;

/* Static string for an error code (never NULL). */
const char *firc_err_str(firc_err_t err);

/* Maps an errno value to the closest firc_err_t. */
firc_err_t firc_err_from_errno(int errnum);

#endif
