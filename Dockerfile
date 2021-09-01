FROM golang AS builder
MAINTAINER sahib@online.de

RUN apt-get update && apt-get install -y \
  libssl-dev \
  ca-certificates \
  fuse

# Build the brig binary:
ENV BRIG_SOURCE /go/src/github.com/sahib/brig
ENV BRIG_BINARY_PATH /usr/bin/brig
COPY . $BRIG_SOURCE
WORKDIR $BRIG_SOURCE
RUN ./scripts/build.sh

# Download IPFS, so the container can startup faster.
# (brig can also download the binary for you, but later)
RUN wget https://dist.ipfs.io/go-ipfs/v0.9.1/go-ipfs_v0.9.1_linux-amd64.tar.gz -O /tmp/ipfs.tar.gz
RUN tar xfv /tmp/ipfs.tar.gz -C /tmp

FROM busybox:1-glibc

# Most test cases can use the pre-defined BRIG_PATH.
ENV BRIG_USER="test@test.com/container"
ENV BRIG_PATH /var/repo
ENV BRIG_MOUNT /mnt/brig
ENV IPFS_PATH /root/.ipfs
RUN mkdir -p $BRIG_PATH

EXPOSE 6666
EXPOSE 4001

COPY --from=builder /usr/bin/brig/brig /usr/local/bin/brig
COPY --from=builder /tmp/go-ipfs/ipfs /usr/local/bin

# This shared lib (part of glibc) doesn't seem to be included with busybox.
COPY --from=builder /lib/*-linux-gnu*/libdl.so.2 /lib/

# Copy over SSL libraries.
COPY --from=builder /usr/lib/*-linux-gnu*/libssl.so* /usr/lib/
COPY --from=builder /usr/lib/*-linux-gnu*/libcrypto.so* /usr/lib/
COPY --from=builder /etc/ssl/certs /etc/ssl/certs
COPY --from=builder /bin/fusermount /usr/local/bin/fusermount

COPY scripts/docker-normal-startup.sh /bin/run.sh
CMD ["/bin/sh", "/bin/run.sh"]

