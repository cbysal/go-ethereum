#!/usr/bin/env bash
set -e

mode="$1"
rate="$2"

if [ -z "$mode" ] || [ -z "$rate" ]; then
	echo "Usage: $0 [0:Geth|1:TxDAG|3:DEPE] [private_rate]"
	exit 1
fi

mkdir -p elog

sync
echo 3 | sudo tee /proc/sys/vm/drop_caches

for ((start = 23000000; start < 24000000; start += 100000)); do
	end=$((start + 10000))

	echo "$mode" "$rate" "$start" "$end"
	./build/bin/depe run-seer "$mode" "$rate" "$start" "$end" "elog/run-seer-$mode-$rate-$start-$end.log"
done
