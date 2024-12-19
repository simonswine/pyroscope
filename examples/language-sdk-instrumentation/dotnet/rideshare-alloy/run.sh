#!/usr/bin/env bash

set -eux

docker-compose build alloy
docker-compose up -d


docker exec -i rideshare-alloy-alloy-1 bash -eux <<"EOF"
for pid in $(pgrep dotnet); do 
  cp -v /libprofilers.so /proc/$pid/root/tmp/
  attach-profiler --socket /proc/$pid/root$(cat /proc/$pid/net/unix | tail -n 1 | awk '{print $8}')
done
EOF
