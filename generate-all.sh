#!/usr/bin/env bash
set -e

mkdir -p elog

for ((start = 23000000; start < 24000000; start += 100000)); do
	end=$((start + 10000))

	echo "$mode" "$start" "$end"
	./build/bin/depe generate-all "$start" "$end" "elog/reexec-$start-$end.log"
done
