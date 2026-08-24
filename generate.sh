#!/usr/bin/env bash
set -e

mode="$1"

if [ -z "$mode" ]; then
	echo "Usage: $0 [0:Geth|1:TxDAG|2:DEPE-o1|3:DEPE-o2]"
	exit 1
fi

mkdir -p elog

sync
echo 3 | sudo tee /proc/sys/vm/drop_caches

for ((start = 23000000; start < 24000000; start += 100000)); do
	end=$((start + 10000))

	echo "$mode" "$start" "$end"
	./build/bin/depe generate "$mode" "$start" "$end" "elog/generate-$mode-$start-$end.log"
done
