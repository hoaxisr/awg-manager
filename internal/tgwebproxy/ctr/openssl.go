//go:build linux && (mipsle || mips) && cgo

package ctr

/*
#include <dlfcn.h>
#include <stddef.h>

#if defined(__GLIBC__)
#define AWGM_LIBCRYPTO "/opt/lib/libcrypto.so.3"
#else
#define AWGM_LIBCRYPTO "/usr/lib/libcrypto.so.3"
#endif

typedef struct evp_cipher_ctx_st EVP_CIPHER_CTX;
typedef struct evp_cipher_st EVP_CIPHER;
typedef struct engine_st ENGINE;
typedef const EVP_CIPHER *(*cipher_fn)(void);
typedef EVP_CIPHER_CTX *(*new_fn)(void);
typedef void (*free_fn)(EVP_CIPHER_CTX *);
typedef int (*init_fn)(EVP_CIPHER_CTX *, const EVP_CIPHER *, ENGINE *, const unsigned char *, const unsigned char *);
typedef int (*update_fn)(EVP_CIPHER_CTX *, unsigned char *, int *, const unsigned char *, int);

static cipher_fn aes256ctr;
static new_fn ctx_new;
static free_fn ctx_free;
static init_fn enc_init;
static update_fn enc_update;

static int awgm_ctr_load(void) {
    void *handle = dlopen(AWGM_LIBCRYPTO, RTLD_NOW | RTLD_LOCAL);
    if (!handle) return 0;
    aes256ctr = (cipher_fn)dlsym(handle, "EVP_aes_256_ctr");
    ctx_new = (new_fn)dlsym(handle, "EVP_CIPHER_CTX_new");
    ctx_free = (free_fn)dlsym(handle, "EVP_CIPHER_CTX_free");
    enc_init = (init_fn)dlsym(handle, "EVP_EncryptInit_ex");
    enc_update = (update_fn)dlsym(handle, "EVP_EncryptUpdate");
    if (aes256ctr && ctx_new && ctx_free && enc_init && enc_update) return 1;
    dlclose(handle);
    return 0;
}

static EVP_CIPHER_CTX *awgm_ctr_new(const unsigned char *key, const unsigned char *iv) {
    EVP_CIPHER_CTX *ctx = ctx_new();
    if (!ctx) return NULL;
    if (enc_init(ctx, aes256ctr(), NULL, key, iv) == 1) return ctx;
    ctx_free(ctx);
    return NULL;
}

static int awgm_ctr_update(EVP_CIPHER_CTX *ctx, unsigned char *dest, const unsigned char *src, int len) {
    if (!ctx) return 0;
    int written = 0;
    return enc_update(ctx, dest, &written, src, len) == 1 && written == len;
}

static void awgm_ctr_free(EVP_CIPHER_CTX *ctx) { ctx_free(ctx); }
*/
import "C"

import (
	"crypto/cipher"
	"runtime"
	"sync"
	"unsafe"
)

var load = sync.OnceValue(func() bool { return C.awgm_ctr_load() == 1 })

type evpStream struct{ ctx *C.EVP_CIPHER_CTX }

func newAccelerated(key, iv []byte) cipher.Stream {
	if !load() {
		return nil
	}
	ctx := C.awgm_ctr_new((*C.uchar)(unsafe.Pointer(&key[0])), (*C.uchar)(unsafe.Pointer(&iv[0])))
	if ctx == nil {
		return nil
	}
	stream := &evpStream{ctx: ctx}
	runtime.SetFinalizer(stream, (*evpStream).free)
	return stream
}

func (s *evpStream) free() {
	if s.ctx != nil {
		C.awgm_ctr_free(s.ctx)
		s.ctx = nil
	}
}

func (s *evpStream) release() {
	runtime.SetFinalizer(s, nil)
	s.free()
}

func (s *evpStream) XORKeyStream(dst, src []byte) {
	if len(src) == 0 {
		return
	}
	if len(dst) < len(src) {
		panic("AES-CTR destination is too short")
	}
	if s.ctx == nil {
		panic("AES-CTR stream already released")
	}
	if C.awgm_ctr_update(s.ctx, (*C.uchar)(unsafe.Pointer(&dst[0])), (*C.uchar)(unsafe.Pointer(&src[0])), C.int(len(src))) != 1 {
		panic("OpenSSL AES-CTR update failed")
	}
	runtime.KeepAlive(s)
}
