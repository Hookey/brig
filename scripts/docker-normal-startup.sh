#!/bin/bash

set -e

if [ -e "${IPFS_PATH}/config" ]; then
  echo "Found IPFS fs-repo at ${IPFS_PATH}"
else
  ipfs init
  ipfs config Addresses.API /ip4/0.0.0.0/tcp/5005
  ipfs config Addresses.Gateway /ip4/0.0.0.0/tcp/8088
fi

brig init --repo $BRIG_PATH --ipfs-path-or-multiaddr $IPFS_PATH $BRIG_USER
brig daemon quit
sleep 2
brig daemon launch -s
