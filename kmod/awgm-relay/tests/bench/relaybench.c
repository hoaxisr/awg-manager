// rb: синтетический WG-поток через релей на loopback. gen -> relay -> sink.
// usage: rb RELAY_PORT SINK_PORT SECONDS SIZE [pps]
#define _GNU_SOURCE
#include <arpa/inet.h>
#include <pthread.h>
#include <stdint.h>
#include <stdio.h>
#include <stdlib.h>
#include <string.h>
#include <sys/socket.h>
#include <sys/time.h>
#include <time.h>
#include <unistd.h>

#define B 32
static volatile long got, gotb; static volatile int stop;
static int sinkfd;
static double now(void){ struct timespec t; clock_gettime(CLOCK_MONOTONIC,&t); return t.tv_sec+t.tv_nsec/1e9; }

static void *sink_loop(void *a){ (void)a;
	static uint8_t buf[B][2048]; struct mmsghdr h[B]; struct iovec v[B];
	struct timeval tv={0,200000}; setsockopt(sinkfd,SOL_SOCKET,SO_RCVTIMEO,&tv,sizeof tv);
	while(!stop){ for(int i=0;i<B;i++){v[i].iov_base=buf[i];v[i].iov_len=2048;memset(&h[i],0,sizeof h[i]);h[i].msg_hdr.msg_iov=&v[i];h[i].msg_hdr.msg_iovlen=1;}
		int n=recvmmsg(sinkfd,h,B,0,NULL); for(int i=0;i<n;i++){got++;gotb+=h[i].msg_len;} }
	return NULL; }

static struct sockaddr_in lo(int p){ struct sockaddr_in a={0}; a.sin_family=AF_INET; a.sin_port=htons(p); a.sin_addr.s_addr=htonl(0x7f000001); return a; }

int main(int c, char **v){
	int rp=atoi(v[1]), sp=atoi(v[2]), secs=atoi(v[3]), size=atoi(v[4]); long pps=c>5?atol(v[5]):0;
	int rcv=4<<20;
	sinkfd=socket(AF_INET,SOCK_DGRAM,0); struct sockaddr_in sa=lo(sp); if(bind(sinkfd,(void*)&sa,sizeof sa)){perror("bind");return 1;}
	setsockopt(sinkfd,SOL_SOCKET,SO_RCVBUF,&rcv,sizeof rcv);
	int g=socket(AF_INET,SOCK_DGRAM,0); struct sockaddr_in ga=lo(0); bind(g,(void*)&ga,sizeof ga);
	struct sockaddr_in ra=lo(rp); connect(g,(void*)&ra,sizeof ra);
	uint8_t hs[148]={1}; send(g,hs,148,0);
	uint8_t tmp[2048]; struct sockaddr_in from; socklen_t fl=sizeof from;
	struct timeval tv={3,0}; setsockopt(sinkfd,SOL_SOCKET,SO_RCVTIMEO,&tv,sizeof tv);
	if(recvfrom(sinkfd,tmp,sizeof tmp,0,(void*)&from,&fl)<0){fprintf(stderr,"нет handshake\n");return 1;}
	if(rp!=sp){ uint8_t r[92]={2}; sendto(sinkfd,r,92,0,(void*)&from,fl); usleep(300000); }
	pthread_t th; pthread_create(&th,NULL,sink_loop,NULL);
	static uint8_t d[B][2048]; struct mmsghdr h[B]; struct iovec iv[B];
	for(int i=0;i<B;i++){ d[i][0]=4; iv[i].iov_base=d[i]; iv[i].iov_len=size; memset(&h[i],0,sizeof h[i]); h[i].msg_hdr.msg_iov=&iv[i]; h[i].msg_hdr.msg_iovlen=1; }
	long sent=0; double t0=now(), end=t0+secs;
	while(now()<end){ int n=sendmmsg(g,h,B,0); if(n>0) sent+=n;
		if(pps){ double due=t0+(double)sent/pps, w=due-now(); if(w>0) usleep(w*1e6); } }
	double el=now()-t0; usleep(500000); stop=1; pthread_join(th,NULL);
	printf("sent=%ld pps  recv=%ld pps  recv=%.1f Mbit/s  loss=%.1f%%\n",(long)(sent/el),(long)(got/el),gotb*8/el/1e6,100.0*(1-(double)got/sent));
	return 0; }
