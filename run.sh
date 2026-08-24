#!/usr/bin/env bash
set -e

mode="$1"
core_count="$2"

if [ -z "$mode" ] || [ -z "$core_count" ]; then
	echo "Usage: $0 [0:Geth|1:TxDAG|2:DEPE-o1|3:DEPE-o2] [core_count]"
	exit 1
fi

mkdir -p elog

core_start=$(($(nproc) - core_count))
core_last=$(($(nproc) - 1))

sync
echo 3 | sudo tee /proc/sys/vm/drop_caches

for ((start = 23000000; start < 24000000; start += 100000)); do
	end=$((start + 10000))

	echo "$mode" "$start" "$end" "$core_count"
	taskset -c "$core_start-$core_last" ./build/bin/depe run "$mode" "$start" "$end" "elog/run-$mode-$start-$end-$core_count.log"
done
