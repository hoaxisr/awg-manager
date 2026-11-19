#include "firc/err.h"

#include <errno.h>

const char *firc_err_str(firc_err_t err)
{
    switch (err) {
    case FIRC_OK:          return "ok";
    case FIRC_ERR_NOMEM:   return "out of memory";
    case FIRC_ERR_INVAL:   return "invalid argument";
    case FIRC_ERR_IO:      return "i/o error";
    case FIRC_ERR_AGAIN:   return "try again";
    case FIRC_ERR_LIMIT:   return "limit reached";
    case FIRC_ERR_TIMEOUT: return "timeout";
    case FIRC_ERR_CLOSED:  return "closed";
    case FIRC_ERR_EXIST:   return "already exists";
    case FIRC_ERR_NOENT:   return "not found";
    case FIRC_ERR_PROTO:   return "protocol error";
    case FIRC_ERR_STATE:   return "invalid state";
    case FIRC_ERR_SYS:     return "system error";
    case FIRC_ERR_UPSTREAM: return "upstream fetch failed";
    case FIRC_ERR_NOSYS:   return "not provided by this kernel";
    case FIRC_ERR_CANCELED: return "canceled";
    }
    return "unknown error";
}

firc_err_t firc_err_from_errno(int errnum)
{
    switch (errnum) {
    case 0:           return FIRC_OK;
    case ENOMEM:      return FIRC_ERR_NOMEM;
    case EINVAL:      return FIRC_ERR_INVAL;
    case EAGAIN:
#if EWOULDBLOCK != EAGAIN
    case EWOULDBLOCK:
#endif
    case EINTR:       return FIRC_ERR_AGAIN;
    case ETIMEDOUT:   return FIRC_ERR_TIMEOUT;
    case EEXIST:      return FIRC_ERR_EXIST;
    case ENOENT:
    case ESRCH:       return FIRC_ERR_NOENT;
    case EPROTO:
    case EBADMSG:     return FIRC_ERR_PROTO;
    case EPIPE:
    case ECONNRESET:
    case EIO:         return FIRC_ERR_IO;
    default:          return FIRC_ERR_SYS;
    }
}
