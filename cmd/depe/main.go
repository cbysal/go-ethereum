package main

import (
	"bufio"
	"bytes"
	"encoding/binary"
	"fmt"
	"io"
	"maps"
	"math/rand"
	"os"
	"path"
	"runtime"
	"slices"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/bits-and-blooms/bitset"
	mapset "github.com/deckarep/golang-set/v2"
	"github.com/ethereum/go-ethereum/cmd/utils"
	"github.com/ethereum/go-ethereum/common"
	"github.com/ethereum/go-ethereum/consensus/misc"
	"github.com/ethereum/go-ethereum/core"
	"github.com/ethereum/go-ethereum/core/rawdb"
	"github.com/ethereum/go-ethereum/core/state"
	"github.com/ethereum/go-ethereum/core/types"
	"github.com/ethereum/go-ethereum/core/vm"
	"github.com/ethereum/go-ethereum/eth/ethconfig"
	"github.com/ethereum/go-ethereum/ethdb"
	"github.com/ethereum/go-ethereum/ethdb/pebble"
	"github.com/ethereum/go-ethereum/internal/flags"
	"github.com/ethereum/go-ethereum/params"
	"github.com/ethereum/go-ethereum/rlp"
	"github.com/holiman/uint256"
	"github.com/urfave/cli/v2"
	"golang.org/x/sync/errgroup"
)

var app = cli.NewApp()

func init() {
	app.Commands = []*cli.Command{
		{
			Name:   "extract-blocks",
			Action: extractBlocks,
			Flags: []cli.Flag{
				utils.DataDirFlag,
			},
		},
		{
			Name:   "make-gas-used",
			Action: makeGasUsed,
			Flags: []cli.Flag{
				utils.DataDirFlag,
			},
		},
		{
			Name:   "extract-private",
			Action: extractPrivate,
		},
		{
			Name:   "preexecute",
			Action: preexecute,
			Flags: []cli.Flag{
				utils.DataDirFlag,
			},
		},
		{
			Name:   "observe",
			Action: observe,
			Flags: []cli.Flag{
				utils.DataDirFlag,
			},
		},
		{
			Name:   "analyze-observe",
			Action: analyzeObserve,
			Flags: []cli.Flag{
				utils.DataDirFlag,
			},
		},
		{
			Name:   "generate",
			Action: generate,
			Flags: []cli.Flag{
				utils.DataDirFlag,
			},
		},
		{
			Name:   "generate-all",
			Action: generateAll,
			Flags: []cli.Flag{
				utils.DataDirFlag,
			},
		},
		{
			Name:   "generate-block-info",
			Action: generateBlockInfo,
			Flags: []cli.Flag{
				utils.DataDirFlag,
			},
		},
		{
			Name:   "analyze-metadata",
			Action: analyzeMetadata,
			Flags: []cli.Flag{
				utils.DataDirFlag,
			},
		},
		{
			Name:   "run",
			Action: run,
			Flags: []cli.Flag{
				utils.DataDirFlag,
			},
		},
		{
			Name:   "run-seer",
			Action: runSeer,
			Flags: []cli.Flag{
				utils.DataDirFlag,
			},
		},
		{
			Name:   "verify",
			Action: verify,
			Flags: []cli.Flag{
				utils.DataDirFlag,
			},
		},
	}
}

func extractBlocks(ctx *cli.Context) error {
	if ctx.NArg() != 3 {
		return fmt.Errorf("got %d arguments, expected 3", ctx.NArg())
	}
	target := ctx.Args().Get(0)
	start, err := strconv.ParseUint(ctx.Args().Get(1), 10, 64)
	if err != nil {
		return err
	}
	end, err := strconv.ParseUint(ctx.Args().Get(2), 10, 64)
	if err != nil {
		return err
	}

	dataDir := ctx.String(utils.DataDirFlag.Name)
	db1, err := pebble.New(path.Join(dataDir, "geth", "chaindata"), 4096, 1024, "", true)
	if err != nil {
		return err
	}
	db2, err := rawdb.Open(db1, rawdb.OpenOptions{
		Ancient:  path.Join(dataDir, "geth", "chaindata", "ancient"),
		ReadOnly: true,
	})
	if err != nil {
		return err
	}
	defer db2.Close()
	if err = os.RemoveAll(target); err != nil {
		return err
	}
	db3, err := pebble.New(path.Join(target, "geth", "chaindata"), 8192, 1024, "", false)
	if err != nil {
		return err
	}
	db4, err := rawdb.Open(db3, rawdb.OpenOptions{
		Ancient: path.Join(target, "geth", "chaindata", "ancient"),
	})
	if err != nil {
		return err
	}
	defer db4.Close()

	kinds := [4]string{
		rawdb.ChainFreezerHashTable,
		rawdb.ChainFreezerHeaderTable,
		rawdb.ChainFreezerBodiesTable,
		rawdb.ChainFreezerReceiptTable,
	}

	if _, err := db4.ModifyAncients(func(op ethdb.AncientWriteOp) error {
		for i := range 3 {
			data, err := db2.Ancient(kinds[i], 0)
			if err != nil {
				return err
			}
			if err := op.AppendRaw(kinds[i], 0, data); err != nil {
				return err
			}
		}
		if err := op.AppendRaw(rawdb.ChainFreezerReceiptTable, 0, []byte{}); err != nil {
			return err
		}
		return nil
	}); err != nil {
		return err
	}
	fmt.Printf("\r%d", 0)

	if _, err = db4.ModifyAncients(func(op ethdb.AncientWriteOp) error {
		for height := uint64(1); height < start; height++ {
			for _, kind := range kinds {
				if err := op.AppendRaw(kind, height, []byte{}); err != nil {
					return err
				}
			}
			if height%1000 == 0 {
				fmt.Printf("\r%d", height)
			}
		}
		return nil
	}); err != nil {
		return err
	}

	const batchSize = uint64(10000)

	chs := make([]chan [][]byte, 3)
	for i := range chs {
		chs[i] = make(chan [][]byte, 2)
	}
	var wg errgroup.Group
	for i, ch := range chs {
		wg.Go(func() error {
			defer close(ch)
			for batchStart := start; batchStart <= end; batchStart += batchSize {
				count := min(batchSize, end-batchStart+1)
				data, err := db2.AncientRange(kinds[i], batchStart, count, 0)
				if err != nil {
					return err
				}
				ch <- data
			}
			return nil
		})
	}
	for batchStart := start; batchStart <= end; batchStart += batchSize {
		count := min(batchSize, end-batchStart+1)
		if _, err := db4.ModifyAncients(func(op ethdb.AncientWriteOp) error {
			for i, ch := range chs {
				for j, data := range <-ch {
					if err := op.AppendRaw(kinds[i], batchStart+uint64(j), data); err != nil {
						return err
					}
				}
			}
			for height := batchStart; height < batchStart+count; height++ {
				if err := op.AppendRaw(rawdb.ChainFreezerReceiptTable, height, []byte{}); err != nil {
					return err
				}
			}
			return nil
		}); err != nil {
			return err
		}
		fmt.Printf("\r%d", batchStart+count-1)
	}
	fmt.Println()

	return db4.Compact(nil, nil)
}

func makeGasUsed(ctx *cli.Context) error {
	if ctx.NArg() != 2 {
		return fmt.Errorf("got %d arguments, expected 2", ctx.NArg())
	}
	start, err := strconv.ParseUint(ctx.Args().Get(0), 0, 64)
	if err != nil {
		return err
	}
	end, err := strconv.ParseUint(ctx.Args().Get(1), 0, 64)
	if err != nil {
		return err
	}

	dataDir := ctx.String(utils.DataDirFlag.Name)
	db1, err := pebble.New(path.Join(dataDir, "geth", "chaindata"), 4096, 1024, "", true)
	if err != nil {
		return err
	}
	db2, err := rawdb.Open(db1, rawdb.OpenOptions{
		Ancient:  path.Join(dataDir, "geth", "chaindata", "ancient"),
		ReadOnly: true,
	})
	if err != nil {
		return err
	}
	defer db2.Close()
	db3, err := pebble.New(path.Join(flags.HomeDir(), "state.slim"), 4096, 1024, "", true)
	if err != nil {
		return err
	}
	defer db3.Close()
	if err = os.RemoveAll("gas-used"); err != nil {
		return err
	}
	db4, err := pebble.New("gas-used", 4096, 1024, "", false)
	if err != nil {
		return err
	}
	defer db4.Close()

	config := params.MainnetChainConfig
	engine, err := ethconfig.CreateConsensusEngine(config, db2)
	if err != nil {
		return err
	}
	chain, err := core.NewHeaderChain(db2, config, engine, func() bool { return false })
	if err != nil {
		return err
	}

	var wg errgroup.Group
	var cur atomic.Uint64
	cur.Store(start + 1)
	for range runtime.NumCPU() {
		wg.Go(func() error {
			sdb := state.NewSlimDatabase(db3)
			processor := core.NewStateProcessor(chain)
			for height := cur.Add(1) - 1; height <= end; height = cur.Add(1) - 1 {
				block := rawdb.ReadBlock(db2, rawdb.ReadCanonicalHash(db2, height), height)
				statedb, err := state.NewSlim(sdb, height-1)
				if err != nil {
					return err
				}
				result, err := processor.Process(block, statedb, vm.Config{})
				if err != nil {
					return err
				}
				gasUsedList := make([]uint64, len(result.Receipts))
				for i, receipt := range result.Receipts {
					gasUsedList[i] = receipt.GasUsed
				}
				gasUsedListBytes, err := rlp.EncodeToBytes(gasUsedList)
				if err != nil {
					return err
				}
				if err = db4.Put(binary.BigEndian.AppendUint64(nil, height), gasUsedListBytes); err != nil {
					return err
				}
				fmt.Printf("\r%d", height)
			}
			return nil
		})
	}
	fmt.Println()

	return db4.Compact(nil, nil)
}

func extractPrivate(ctx *cli.Context) error {
	if ctx.NArg() != 2 {
		return fmt.Errorf("got %d arguments, expected 2", ctx.NArg())
	}
	start, err := strconv.ParseUint(ctx.Args().Get(0), 10, 64)
	if err != nil {
		return err
	}
	end, err := strconv.ParseUint(ctx.Args().Get(1), 10, 64)
	if err != nil {
		return err
	}

	file, err := os.Open("result/private-txs.full.csv")
	if err != nil {
		return err
	}
	defer file.Close()
	reader := bufio.NewReader(file)
	if err = os.RemoveAll(path.Join(flags.HomeDir(), "private-txs")); err != nil {
		return err
	}
	db, err := pebble.New(path.Join(flags.HomeDir(), "private-txs"), 8192, 1024, "", false)
	if err != nil {
		return err
	}
	defer db.Close()

	for {
		line, err := reader.ReadString('\n')
		if err == io.EOF {
			break
		}
		if err != nil {
			return err
		}
		if len(line) == 0 {
			continue
		}
		splits := strings.Split(strings.TrimSpace(line), ",")
		height, err := strconv.ParseUint(splits[0], 10, 64)
		if err != nil {
			return err
		}
		fmt.Printf("\r%d", height)
		if height <= start || height > end {
			continue
		}
		txs := make([]uint, 0, len(splits)-1)
		for i := 3; i < len(splits); i++ {
			tx, err := strconv.Atoi(splits[i])
			if err != nil {
				return err
			}
			txs = append(txs, uint(tx))
		}
		txsBytes, err := rlp.EncodeToBytes(txs)
		if err != nil {
			return err
		}
		if err = db.Put(binary.BigEndian.AppendUint64(nil, height), txsBytes); err != nil {
			return err
		}
	}
	fmt.Println()

	return db.Compact(nil, nil)
}

func preexecute(ctx *cli.Context) error {
	if ctx.NArg() != 4 {
		return fmt.Errorf("got %d arguments, expected 4", ctx.NArg())
	}
	mode, err := strconv.Atoi(ctx.Args().Get(0))
	if err != nil {
		return err
	}
	start, err := strconv.ParseUint(ctx.Args().Get(1), 10, 64)
	if err != nil {
		return err
	}
	end, err := strconv.ParseUint(ctx.Args().Get(2), 10, 64)
	if err != nil {
		return err
	}
	file, err := os.Create(ctx.Args().Get(3))
	if err != nil {
		return err
	}
	defer file.Close()
	writer := bufio.NewWriter(file)
	defer writer.Flush()

	dataDir := ctx.String(utils.DataDirFlag.Name)
	db1, err := pebble.New(path.Join(dataDir, "geth", "chaindata"), 0, 0, "", true)
	if err != nil {
		return err
	}
	db2, err := rawdb.Open(db1, rawdb.OpenOptions{
		Ancient:  path.Join(dataDir, "geth", "chaindata", "ancient"),
		ReadOnly: true,
	})
	if err != nil {
		return err
	}
	defer db2.Close()
	db3, err := pebble.New(path.Join(flags.HomeDir(), "state.slim"), 4096, 1024, "", true)
	if err != nil {
		return err
	}
	defer db3.Close()
	db4, err := pebble.New(path.Join(flags.HomeDir(), "private-txs"), 0, 0, "", true)
	if err != nil {
		return err
	}
	defer db4.Close()

	config := params.MainnetChainConfig
	engine, err := ethconfig.CreateConsensusEngine(config, db2)
	if err != nil {
		return err
	}
	chain, err := core.NewHeaderChain(db2, config, engine, nil)
	if err != nil {
		return err
	}
	processor := core.NewStateProcessor(chain)

	sdb := state.NewSlimDatabase(db3)

	for height := start - 100000 + 1; height <= start; height++ {
		block := rawdb.ReadBlock(db2, rawdb.ReadCanonicalHash(db2, height), height)
		statedb, err := state.NewSlim(sdb, height-1)
		if err != nil {
			return err
		}
		if _, err = processor.Process(block, statedb, vm.Config{}); err != nil {
			return err
		}
		fmt.Printf("\r%d", height)
	}

	ch := make(chan common.Pair[*types.Block, *bitset.BitSet], 100)
	var wg errgroup.Group
	wg.Go(func() error {
		for height := start + 1; height <= end; height++ {
			block := rawdb.ReadBlock(db2, rawdb.ReadCanonicalHash(db2, height), height)
			privates := bitset.New(uint(block.Transactions().Len()))
			if mode == 2 {
				privateTxsBytes, err := db4.Get(binary.BigEndian.AppendUint64(nil, block.NumberU64()))
				if err != nil {
					return err
				}
				var txs []uint
				if err = rlp.DecodeBytes(privateTxsBytes, &txs); err != nil {
					return err
				}
				for _, tx := range txs {
					privates.Set(tx)
				}
			}
			ch <- common.Pair[*types.Block, *bitset.BitSet]{First: block, Second: privates}
		}
		close(ch)
		return nil
	})

	branchTable := vm.CreateNewTable()

	for p := range ch {
		block := p.First
		privates := p.Second
		height := block.NumberU64()
		statedb, err := state.NewSlim(sdb, height-1)
		if err != nil {
			return err
		}
		var latency time.Duration
		if mode == 0 {
			ts := time.Now()
			if _, err = processor.Process(block, statedb, vm.Config{}); err != nil {
				fmt.Println(1, err)
			}
			latency = time.Since(ts)
		} else {
			mvCache := vm.NewMVCache()
			preTable := vm.NewPreExecutionTable()
			if _, err = processor.Process(block, statedb.Copy(), vm.Config{
				IsSeer:            true,
				IsPreExec:         true,
				MVCache:           mvCache,
				VarTable:          branchTable,
				PreExecutionTable: preTable,
				Privates:          privates,
			}); err != nil {
				return err
			}

			ts := time.Now()
			if _, err = processor.Process(block, statedb, vm.Config{
				IsSeer:            true,
				MVCache:           mvCache,
				VarTable:          branchTable,
				PreExecutionTable: preTable,
				Privates:          privates,
			}); err != nil {
				return err
			}
			latency = time.Since(ts)
			branchTable.Sweep()
		}
		if _, err = fmt.Fprintln(writer, latency.Nanoseconds()); err != nil {
			return err
		}

		fmt.Printf("\r%d", height)
	}
	fmt.Println()

	return nil
}

func observe(ctx *cli.Context) error {
	if ctx.NArg() != 2 {
		return nil
	}
	start, err := strconv.ParseUint(ctx.Args().Get(0), 10, 64)
	if err != nil {
		return err
	}
	end, err := strconv.ParseUint(ctx.Args().Get(1), 10, 64)
	if err != nil {
		return err
	}

	dataDir := ctx.String(utils.DataDirFlag.Name)
	db1, err := pebble.New(path.Join(dataDir, "geth", "chaindata"), 4096, 1024, "", true)
	if err != nil {
		return err
	}
	db2, err := rawdb.Open(db1, rawdb.OpenOptions{
		Ancient:  path.Join(dataDir, "geth", "chaindata", "ancient"),
		ReadOnly: true,
	})
	if err != nil {
		return err
	}
	defer db2.Close()
	db3, err := pebble.New(path.Join(flags.HomeDir(), "state.slim"), 4096, 1024, "", true)
	if err != nil {
		return err
	}
	defer db3.Close()
	config := params.MainnetChainConfig
	engine, err := ethconfig.CreateConsensusEngine(config, db2)
	if err != nil {
		return err
	}
	chain, err := core.NewHeaderChain(db2, config, engine, func() bool { return false })
	if err != nil {
		return err
	}

	if err = os.RemoveAll("observe"); err != nil {
		return err
	}
	db5, err := pebble.New("observe", 4096, 1024, "", false)
	if err != nil {
		return err
	}
	defer db5.Close()

	var wg errgroup.Group
	var cur atomic.Uint64
	cur.Store(start + 1)
	for range runtime.NumCPU() {
		wg.Go(func() error {
			sdb := state.NewSlimDatabase(db3)
			for height := cur.Add(1) - 1; height <= end; height = cur.Add(1) - 1 {
				block := rawdb.ReadBlock(db2, rawdb.ReadCanonicalHash(db2, height), height)

				conflicts1, gasUsedList, accountReadSetList, accountWriteSetList, storageReadSetList, storageWriteSetList, err := generateTxDAGHelper(chain, sdb, block)
				if err != nil {
					return err
				}

				conflicts2, _, err := generateTapes(chain, sdb, block, 2, true)
				if err != nil {
					return err
				}

				maxPath := conflicts2.MaxWeightPath(gasUsedList)
				pathSet1 := mapset.NewThreadUnsafeSet[int]()
				pathSet1.Append(maxPath...)
				accounts1 := make(map[common.Address]struct{})
				storages1 := make(map[common.AddrHash]struct{})
				accounts2 := make(map[common.Address]struct{})
				storages2 := make(map[common.AddrHash]struct{})
				for _, accountSetList := range [][]map[common.Address]struct{}{accountReadSetList, accountWriteSetList} {
					for i, accountSet := range accountSetList {
						for addr := range accountSet {
							accounts1[addr] = struct{}{}
							if pathSet1.Contains(i) {
								accounts2[addr] = struct{}{}
							}
						}
					}
				}
				for _, storageSetList := range [][]map[common.AddrHash]struct{}{storageReadSetList, storageWriteSetList} {
					for i, storageSet := range storageSetList {
						for addrHash := range storageSet {
							accounts1[addrHash.Addr] = struct{}{}
							storages1[addrHash] = struct{}{}
							if pathSet1.Contains(i) {
								accounts2[addrHash.Addr] = struct{}{}
								storages2[addrHash] = struct{}{}
							}
						}
					}
				}
				stateNum1 := uint64(len(accounts1) + len(storages1))
				stateNum2 := uint64(len(accounts2) + len(storages2))

				for i, obj := range []any{gasUsedList, conflicts1, conflicts2, &stateNum1, &stateNum2} {
					key := binary.BigEndian.AppendUint64([]byte{byte(i)}, height)
					value, err := rlp.EncodeToBytes(&obj)
					if err != nil {
						return err
					}
					if err = db5.Put(key, value); err != nil {
						return err
					}
				}

				fmt.Printf("\r%d", height)
			}
			return nil
		})
	}
	if err = wg.Wait(); err != nil {
		return err
	}
	fmt.Println()

	return db5.Compact(nil, nil)
}

func analyzeObserve(ctx *cli.Context) error {
	if ctx.NArg() != 2 {
		return nil
	}
	start, err := strconv.ParseUint(ctx.Args().Get(0), 10, 64)
	if err != nil {
		return err
	}
	end, err := strconv.ParseUint(ctx.Args().Get(1), 10, 64)
	if err != nil {
		return err
	}

	db, err := pebble.New("observe", 4096, 1024, "", false)
	if err != nil {
		return err
	}
	defer db.Close()

	depNums1, depNums2 := make([]int, end-start), make([]int, end-start)
	stateNums1, stateNums2 := make([]int, end-start), make([]int, end-start)
	gasUsedList1, gasUsedList2, gasUsedList3, gasUsedList4 := make([]uint64, end-start), make([]uint64, end-start), make([]uint64, end-start), make([]uint64, end-start)
	var wg errgroup.Group
	var cur atomic.Uint64
	cur.Store(start + 1)
	for range runtime.NumCPU() {
		wg.Go(func() error {
			for height := cur.Add(1) - 1; height <= end; height = cur.Add(1) - 1 {
				var gasUsedList []uint64
				var conflicts1, conflicts2 types.Conflicts
				value, err := db.Get(binary.BigEndian.AppendUint64([]byte{0}, height))
				if err != nil {
					panic(err)
				}
				if err = rlp.DecodeBytes(value, &gasUsedList); err != nil {
					panic(err)
				}
				for j, conflicts := range []*types.Conflicts{&conflicts1, &conflicts2} {
					value, err = db.Get(binary.BigEndian.AppendUint64([]byte{byte(j + 1)}, height))
					if err != nil {
						panic(err)
					}
					if err = rlp.DecodeBytes(value, conflicts); err != nil {
						panic(err)
					}
				}
				var stateNum1, stateNum2 uint64
				value, err = db.Get(binary.BigEndian.AppendUint64([]byte{3}, height))
				if err != nil {
					panic(err)
				}
				if err = rlp.DecodeBytes(value, &stateNum1); err != nil {
					panic(err)
				}
				value, err = db.Get(binary.BigEndian.AppendUint64([]byte{4}, height))
				if err != nil {
					panic(err)
				}
				if err = rlp.DecodeBytes(value, &stateNum2); err != nil {
					panic(err)
				}

				depNums1[height-start-1] = conflicts1.DepNum()
				depNums2[height-start-1] = conflicts2.DepNum()

				stateNums1[height-start-1] = int(stateNum1)
				stateNums2[height-start-1] += int(stateNum2)

				path1 := conflicts1.MaxWeightPath(gasUsedList)
				paths := conflicts2.TopWeightPaths(gasUsedList, 2)
				for _, pathLen := range gasUsedList {
					gasUsedList1[height-start-1] += pathLen
				}
				for _, node := range path1 {
					gasUsedList2[height-start-1] += gasUsedList[node]
				}
				for _, node := range paths[0] {
					gasUsedList3[height-start-1] += gasUsedList[node]
				}
				for _, node := range paths[1] {
					gasUsedList4[height-start-1] += gasUsedList[node]
				}
				fmt.Printf("%d\r", height)
			}
			return nil
		})
	}
	if err = wg.Wait(); err != nil {
		return err
	}

	file, err := os.Create("result/observe-dep-num.csv")
	if err != nil {
		return err
	}
	writer := bufio.NewWriter(file)
	for height := range end - start {
		if _, err = fmt.Fprintf(writer, "%d,%d\n", depNums1[height], depNums2[height]); err != nil {
			return err
		}
	}
	if err = writer.Flush(); err != nil {
		return err
	}
	if err = file.Close(); err != nil {
		return err
	}

	file, err = os.Create("result/observe-state-num.csv")
	if err != nil {
		return err
	}
	writer = bufio.NewWriter(file)
	for height := range end - start {
		if _, err = fmt.Fprintf(writer, "%d,%d\n", stateNums1[height], stateNums2[height]); err != nil {
			return err
		}
	}
	if err = writer.Flush(); err != nil {
		return err
	}
	if err = file.Close(); err != nil {
		return err
	}

	file, err = os.Create("result/observe-gas-used.csv")
	if err != nil {
		return err
	}
	writer = bufio.NewWriter(file)
	for height := range end - start {
		if _, err = fmt.Fprintf(writer, "%d,%d,%d,%d\n", gasUsedList1[height], gasUsedList2[height], gasUsedList3[height], gasUsedList4[height]); err != nil {
			return err
		}
	}
	if err = writer.Flush(); err != nil {
		return err
	}
	if err = file.Close(); err != nil {
		return err
	}
	return nil
}

func generateGeth(chain core.ChainContext, sdb *state.SlimDatabase, block *types.Block) error {
	height := block.NumberU64()

	statedb, err := state.NewSlim(sdb, height-1)
	if err != nil {
		return err
	}
	processor := core.NewStateProcessor(chain)

	if _, err = processor.Process(block, statedb, vm.Config{}); err != nil {
		return err
	}

	return nil
}

func generateTxDAGHelper(chain core.ChainContext, sdb *state.SlimDatabase, block *types.Block) (*types.Conflicts, []uint64, []map[common.Address]struct{}, []map[common.Address]struct{}, []map[common.AddrHash]struct{}, []map[common.AddrHash]struct{}, error) {
	config := chain.Config()
	height := block.NumberU64()
	txNum := block.Transactions().Len()

	context := core.NewEVMBlockContext(block.Header(), chain, nil)
	signer := types.MakeSigner(config, block.Number(), block.Time())

	statedb, err := state.NewSlim(sdb, height-1)
	if err != nil {
		return nil, nil, nil, nil, nil, nil, err
	}

	gp := new(core.GasPool).AddGas(block.GasLimit())
	evm := vm.NewEVM(context, statedb, config, vm.Config{})

	if config.DAOForkSupport && config.DAOForkBlock != nil && config.DAOForkBlock.Cmp(block.Number()) == 0 {
		misc.ApplyDAOHardFork(statedb)
	}

	if beaconRoot := block.BeaconRoot(); beaconRoot != nil {
		core.ProcessBeaconBlockRoot(*beaconRoot, evm)
	}
	if config.IsPrague(block.Number(), block.Time()) || config.IsVerkle(block.Number(), block.Time()) {
		core.ProcessParentBlockHash(block.ParentHash(), evm)
	}

	accountReadsList := make([]map[common.Address]struct{}, txNum)
	accountWritesList := make([]map[common.Address]struct{}, txNum)
	storageReadsList := make([]map[common.AddrHash]struct{}, txNum)
	storageWritesList := make([]map[common.AddrHash]struct{}, txNum)
	gasUsedList := make([]uint64, block.Transactions().Len())
	for i, tx := range block.Transactions() {
		msg, err := core.TransactionToMessage(tx, signer, block.BaseFee())
		if err != nil {
			return nil, nil, nil, nil, nil, nil, err
		}
		statedb.AccountReadSet = make(map[common.Address]struct{})
		statedb.AccountWriteSet = make(map[common.Address]struct{})
		statedb.StorageReadSet = make(map[common.AddrHash]struct{})
		statedb.StorageWriteSet = make(map[common.AddrHash]struct{})
		result, err := core.ApplyMessage(evm, msg, gp, false, 0, nil)
		if err != nil {
			return nil, nil, nil, nil, nil, nil, err
		}
		accountReadsList[i] = statedb.AccountReadSet
		accountWritesList[i] = statedb.AccountWriteSet
		storageReadsList[i] = statedb.StorageReadSet
		storageWritesList[i] = statedb.StorageWriteSet
		statedb.Finalise(config.IsEIP158(block.Number()))
		gasUsedList[i] = result.UsedGas
	}

	conflicts := types.NewConflicts(txNum)
	for i := range txNum {
		for j := i + 1; j < txNum; j++ {
			for addr := range accountWritesList[i] {
				if _, ok := accountReadsList[j][addr]; ok {
					conflicts.Add(i, j)
					break
				}
			}
			for addrHash := range storageWritesList[i] {
				if _, ok := storageReadsList[j][addrHash]; ok {
					conflicts.Add(i, j)
					break
				}
			}
		}
	}

	return conflicts, gasUsedList, accountReadsList, accountWritesList, storageReadsList, storageWritesList, nil
}

func generateTxDAG(chain core.ChainContext, sdb *state.SlimDatabase, block *types.Block) (*types.Conflicts, error) {
	conflicts, _, _, _, _, _, err := generateTxDAGHelper(chain, sdb, block)
	return conflicts, err
}

var ReExecCount atomic.Uint64

func generateTapesHelper(chain core.ChainContext, sdb *state.SlimDatabase, block *types.Block, mode int, prune bool) (*state.StateDB, *types.Conflicts, *types.PreloadSlots, error) {
	config := chain.Config()
	height := block.NumberU64()
	txNum := block.Transactions().Len()

	context := core.NewEVMBlockContext(block.Header(), chain, nil)
	signer := types.MakeSigner(config, block.Number(), block.Time())

	conflicts := types.NewConflicts(txNum)
	statedb, err := state.NewSlim(sdb, height-1)
	if err != nil {
		return nil, nil, nil, err
	}

	gp := new(core.GasPool).AddGas(block.GasLimit())
	evm := vm.NewEVM(context, statedb, config, vm.Config{})

	if mode == 3 {
		evm.Keccak256Hashes = make(map[common.Hash][]common.Hash)
	}

	if config.DAOForkSupport && config.DAOForkBlock != nil && config.DAOForkBlock.Cmp(block.Number()) == 0 {
		misc.ApplyDAOHardFork(statedb)
	}

	if beaconRoot := block.BeaconRoot(); beaconRoot != nil {
		core.ProcessBeaconBlockRoot(*beaconRoot, evm)
	}
	if config.IsPrague(block.Number(), block.Time()) || config.IsVerkle(block.Number(), block.Time()) {
		core.ProcessParentBlockHash(block.ParentHash(), evm)
	}

	msgs := make([]*core.Message, txNum)
	statedbs := make([]*state.StateDB, txNum)

	statedbBase := statedb.Copy()

	gasUsedList := make([]uint64, block.Transactions().Len())
	statedbCopy := statedbBase.Copy()
	evm.StateDB = statedbCopy

	var wg errgroup.Group
	completes := make([]chan struct{}, txNum)
	for i := range txNum {
		completes[i] = make(chan struct{})
	}
	wg.Go(func() error {
		var allLogs []*types.Log
		for i, tx := range block.Transactions() {
			msg, err := core.TransactionToMessage(tx, signer, block.BaseFee())
			if err != nil {
				return err
			}
			msgs[i] = msg
			statedbCopy.SetTxContext(tx.Hash(), i)
			statedbCopy.GetNonces = make(map[common.Address]struct{})
			statedbCopy.GetBalances = make(map[common.Address]struct{})
			statedbCopy.GetCodes = make(map[common.Address]struct{})
			statedbCopy.GetStates = make(map[common.Address]map[common.Hash]struct{})
			statedbCopy.Exists = make(map[common.Address]struct{})
			statedbCopy.Empties = make(map[common.Address]struct{})
			result, err := core.ApplyMessage(evm, msg, gp, false, 0, nil)
			if err != nil {
				return err
			}
			statedbs[i] = statedbCopy.CopyForMerge()
			close(completes[i])
			statedbCopy.Finalise(config.IsEIP158(block.Number()))
			gasUsedList[i] = result.UsedGas
			allLogs = append(allLogs, statedbCopy.GetLogs(tx.Hash(), block.NumberU64(), block.Hash(), block.Time())...)
		}
		statedbCopy.GetNonces = nil
		statedbCopy.GetBalances = nil
		statedbCopy.GetCodes = nil
		statedbCopy.GetStates = nil
		statedbCopy.Exists = nil
		statedbCopy.Empties = nil
		var requests [][]byte
		if config.IsPrague(block.Number(), block.Time()) {
			evm.Config.IsSeer = false
			evm.Config.IsPreExec = false
			evm.SetCallMap(nil)
			requests = [][]byte{}
			if err := core.ParseDepositLogs(&requests, allLogs, config); err != nil {
				return fmt.Errorf("failed to parse deposit logs: %w", err)
			}
			if err := core.ProcessWithdrawalQueue(&requests, evm); err != nil {
				return fmt.Errorf("failed to process withdrawal queue: %w", err)
			}
			if err := core.ProcessConsolidationQueue(&requests, evm); err != nil {
				return fmt.Errorf("failed to process consolidation queue: %w", err)
			}
		}
		chain.Engine().Finalize(chain, block.Header(), statedbCopy, block.Body())
		return nil
	})

	createAccountsList := make([]map[common.Address]struct{}, txNum)
	setNoncesList := make([]map[common.Address]uint64, txNum)
	setBalancesList := make([]map[common.Address]*uint256.Int, txNum)
	setCodesList := make([]map[common.Address][]byte, txNum)
	setStatesList := make([]map[common.Address]state.Storage, txNum)
	selfDestructsList := make([]map[common.Address]struct{}, txNum)
	for i, tx := range block.Transactions() {
		wg.Go(func() error {
			<-completes[i]
			createAccountsList[i], setNoncesList[i], setBalancesList[i], setCodesList[i], setStatesList[i], selfDestructsList[i] = statedbs[i].WriteMap()
			flag := false
			for j := range i {
				for addr := range statedbs[i].GetNonces {
					if setNoncesList[j][addr] != 0 {
						conflicts.Add(j, i)
						flag = true
						break
					}
				}
				for addr := range statedbs[i].GetCodes {
					if _, ok := setCodesList[j][addr]; ok {
						conflicts.Add(j, i)
						flag = true
						break
					}
				}
			}
			if !flag {
				for j := range i {
					for addr := range statedbs[i].GetBalances {
						if balance, ok := setBalancesList[j][addr]; ok && !balance.IsZero() {
							flag = true
							break
						}
					}
					for addr, keys := range statedbs[i].GetStates {
						for key := range keys {
							if setStatesList[j][addr][key] != (common.Hash{}) {
								flag = true
								break
							}
						}
						if flag {
							break
						}
					}
					for addr := range statedbs[i].Exists {
						if _, ok := createAccountsList[j][addr]; ok {
							flag = true
							break
						}
						if _, ok := setNoncesList[j][addr]; ok {
							flag = true
							break
						}
						if _, ok := setBalancesList[j][addr]; ok {
							flag = true
							break
						}
						if _, ok := setCodesList[j][addr]; ok {
							flag = true
							break
						}
						if _, ok := setStatesList[j][addr]; ok {
							flag = true
							break
						}
						if _, ok := selfDestructsList[j][addr]; ok {
							flag = true
							break
						}
					}
					for addr := range statedbs[i].Empties {
						if _, ok := setNoncesList[j][addr]; ok {
							flag = true
							break
						}
						if _, ok := setBalancesList[j][addr]; ok {
							flag = true
							break
						}
						if _, ok := setCodesList[j][addr]; ok {
							flag = true
							break
						}
						if _, ok := setStatesList[j][addr]; ok {
							flag = true
							break
						}
					}
					if flag {
						break
					}
					for addr := range selfDestructsList[j] {
						if _, ok := createAccountsList[i][addr]; ok {
							flag = true
							break
						}
						if _, ok := statedbs[i].GetNonces[addr]; ok {
							flag = true
							break
						}
						if _, ok := statedbs[i].GetBalances[addr]; ok {
							flag = true
							break
						}
						if _, ok := statedbs[i].GetCodes[addr]; ok {
							flag = true
							break
						}
						if _, ok := statedbs[i].GetStates[addr]; ok {
							flag = true
							break
						}
					}
					if flag {
						break
					}
				}
			}
			if !flag {
				return nil
			}

			gp := new(core.GasPool).AddGas(block.GasLimit())
			evm := vm.NewEVM(context, nil, config, vm.Config{})
			for stage := 0; stage < 6; stage++ {
				statedbCopy := statedbBase.Copy()
				statedbCopy.SetTxContext(tx.Hash(), i)
				for _, j := range conflicts.DirectAncestors(i) {
					statedbCopy.MergeState(statedbs[j])
				}

				evm.StateDB = statedbCopy
				core.ApplyMessage(evm, msgs[i], gp, false, 0, nil)
				ReExecCount.Add(1)

				createAccounts, setNonces, setBalances, setCodes, setStates, selfDestructs := statedbCopy.WriteMap()

				if maps.Equal(createAccountsList[i], createAccounts) &&
					maps.Equal(setNoncesList[i], setNonces) &&
					maps.EqualFunc(setBalancesList[i], setBalances, func(a, b *uint256.Int) bool { return a.Eq(b) }) &&
					maps.EqualFunc(setCodesList[i], setCodes, bytes.Equal) &&
					maps.EqualFunc(setStatesList[i], setStates, func(a, b state.Storage) bool { return maps.Equal(a, b) }) &&
					maps.Equal(selfDestructsList[i], selfDestructs) {
					break
				}

				flag = true
				for flag && stage < 6 {
					switch stage {
					case 0:
						addrs := make(map[common.Address]struct{})
						for addr := range setStatesList[i] {
							addrs[addr] = struct{}{}
						}
						for addr := range setStates {
							addrs[addr] = struct{}{}
						}
						for addr := range addrs {
							keys := make(map[common.Hash]struct{})
							for key := range setStatesList[i][addr] {
								keys[key] = struct{}{}
							}
							for key := range setStates[addr] {
								keys[key] = struct{}{}
							}
							for key := range keys {
								if setStatesList[i][addr][key] == setStates[addr][key] {
									continue
								}
								for j := range i {
									if setStatesList[j][addr][key] != (common.Hash{}) {
										if conflicts.Add(j, i) {
											flag = false
										}
									}
								}
							}
						}
					case 1:
						for addr := range statedbs[i].GetBalances {
							for j := range i {
								if balance, ok := setBalancesList[j][addr]; ok && !balance.IsZero() {
									if conflicts.Add(j, i) {
										flag = false
									}
								}
							}
						}
					case 2:
						for addr, keys := range statedbs[i].GetStates {
							for key := range keys {
								for j := range i {
									if setStatesList[j][addr][key] != (common.Hash{}) {
										if conflicts.Add(j, i) {
											flag = false
										}
									}
								}
							}
						}
					case 3:
						for addr := range statedbs[i].Exists {
							for j := range i {
								if _, ok := createAccountsList[j][addr]; ok {
									if conflicts.Add(j, i) {
										flag = false
									}
									continue
								}
								if _, ok := setNoncesList[j][addr]; ok {
									if conflicts.Add(j, i) {
										flag = false
									}
									continue
								}
								if _, ok := setBalancesList[j][addr]; ok {
									if conflicts.Add(j, i) {
										flag = false
									}
									continue
								}
								if _, ok := setCodesList[j][addr]; ok {
									if conflicts.Add(j, i) {
										flag = false
									}
									continue
								}
								if _, ok := setStatesList[j][addr]; ok {
									if conflicts.Add(j, i) {
										flag = false
									}
									continue
								}
								if _, ok := selfDestructsList[j][addr]; ok {
									if conflicts.Add(j, i) {
										flag = false
									}
									continue
								}
							}
						}
					case 4:
						for addr := range statedbs[i].Empties {
							for j := range i {
								if _, ok := setNoncesList[j][addr]; ok {
									if conflicts.Add(j, i) {
										flag = false
									}
									continue
								}
								if _, ok := setBalancesList[j][addr]; ok {
									if conflicts.Add(j, i) {
										flag = false
									}
									continue
								}
								if _, ok := setCodesList[j][addr]; ok {
									if conflicts.Add(j, i) {
										flag = false
									}
									continue
								}
								if _, ok := setStatesList[j][addr]; ok {
									if conflicts.Add(j, i) {
										flag = false
									}
									continue
								}
							}
						}
					case 5:
						for j := range i {
							for addr := range selfDestructsList[j] {
								if _, ok := createAccountsList[i][addr]; ok {
									if conflicts.Add(j, i) {
										flag = false
									}
									break
								}
								if _, ok := statedbs[i].GetNonces[addr]; ok {
									if conflicts.Add(j, i) {
										flag = false
									}
									break
								}
								if _, ok := statedbs[i].GetBalances[addr]; ok {
									if conflicts.Add(j, i) {
										flag = false
									}
									break
								}
								if _, ok := statedbs[i].GetCodes[addr]; ok {
									if conflicts.Add(j, i) {
										flag = false
									}
									break
								}
								if _, ok := statedbs[i].GetStates[addr]; ok {
									if conflicts.Add(j, i) {
										flag = false
									}
									break
								}
							}
						}
					}
					if flag {
						stage++
					}
				}
			}
			return nil
		})
	}
	if err = wg.Wait(); err != nil {
		return nil, nil, nil, err
	}

	if prune {
		for i, tx := range block.Transactions() {
			wg.Go(func() error {
				gp := new(core.GasPool).AddGas(block.GasLimit())
				evm := vm.NewEVM(context, nil, config, vm.Config{})
				for j := conflicts.DirectAncestorNum(i) - 1; j >= 0; j-- {
					statedbCopy := statedbBase.Copy()
					statedbCopy.SetTxContext(tx.Hash(), i)
					for k, txid := range conflicts.DirectAncestors(i) {
						if k != j {
							statedbCopy.MergeState(statedbs[txid])
						}
					}

					evm.StateDB = statedbCopy
					core.ApplyMessage(evm, msgs[i], gp, false, 0, nil)

					createAccounts1, nonceWrites1, balanceWrites1, codeWrites1, storageWrites1, selfDestructs1 := statedbCopy.WriteMap()

					if maps.Equal(createAccountsList[i], createAccounts1) &&
						maps.Equal(setNoncesList[i], nonceWrites1) &&
						maps.EqualFunc(setBalancesList[i], balanceWrites1, func(a, b *uint256.Int) bool { return a.Eq(b) }) &&
						maps.EqualFunc(setCodesList[i], codeWrites1, bytes.Equal) &&
						maps.EqualFunc(setStatesList[i], storageWrites1, func(a, b state.Storage) bool { return maps.Equal(a, b) }) &&
						maps.Equal(selfDestructsList[i], selfDestructs1) {
						conflicts.Remove(conflicts.DirectAncestors(i)[j], i)
					}
				}
				return nil
			})
		}
		if err = wg.Wait(); err != nil {
			return nil, nil, nil, err
		}
	}

	var ps *types.PreloadSlots
	if mode == 3 {
		preloadSlots := make(map[common.Address]mapset.Set[common.Hash])
		maxWeightPath := conflicts.MaxWeightPath(gasUsedList)
		for _, txid := range maxWeightPath {
			for _, addrs := range []map[common.Address]struct{}{statedbs[txid].GetNonces, statedbs[txid].GetBalances, statedbs[txid].GetCodes, statedbs[txid].Exists, statedbs[txid].Empties} {
				for addr := range addrs {
					if _, ok := preloadSlots[addr]; !ok {
						preloadSlots[addr] = mapset.NewSet[common.Hash]()
					}
				}
			}
			for addr, keys := range statedbs[txid].GetStates {
				if _, ok := preloadSlots[addr]; !ok {
					preloadSlots[addr] = mapset.NewSet[common.Hash]()
				}
				for key := range keys {
					preloadSlots[addr].Add(key)
				}
			}
		}

		slotsList := make([]common.Pair[common.Address, []common.Hash], 0)
		for addr, hashes := range preloadSlots {
			sortedHashes := hashes.ToSlice()
			slices.SortFunc(sortedHashes, common.Hash.Cmp)
			slotsList = append(slotsList, common.Pair[common.Address, []common.Hash]{
				First:  addr,
				Second: sortedHashes,
			})
		}
		slices.SortFunc(slotsList, func(a, b common.Pair[common.Address, []common.Hash]) int {
			return a.First.Cmp(b.First)
		})
		ps = &types.PreloadSlots{SlotsList: slotsList, Hashes: evm.Keccak256Hashes}
	}

	return statedbCopy, conflicts, ps, nil
}

func generateTapes(chain core.ChainContext, sdb *state.SlimDatabase, block *types.Block, mode int, prune bool) (*types.Conflicts, *types.PreloadSlots, error) {
	_, conflicts, preloadSlots, err := generateTapesHelper(chain, sdb, block, mode, prune)
	return conflicts, preloadSlots, err
}

func generate(ctx *cli.Context) error {
	if ctx.NArg() != 4 {
		return nil
	}
	mode, err := strconv.Atoi(ctx.Args().Get(0))
	if err != nil {
		return err
	}
	start, err := strconv.ParseUint(ctx.Args().Get(1), 10, 64)
	if err != nil {
		return err
	}
	end, err := strconv.ParseUint(ctx.Args().Get(2), 10, 64)
	if err != nil {
		return err
	}
	file, err := os.Create(ctx.Args().Get(3))
	if err != nil {
		return err
	}
	defer file.Close()
	writer := bufio.NewWriter(file)
	defer writer.Flush()

	dataDir := ctx.String(utils.DataDirFlag.Name)
	db1, err := pebble.New(path.Join(dataDir, "geth", "chaindata"), 4096, 1024, "", true)
	if err != nil {
		return err
	}
	db2, err := rawdb.Open(db1, rawdb.OpenOptions{
		Ancient:  path.Join(dataDir, "geth", "chaindata", "ancient"),
		ReadOnly: true,
	})
	if err != nil {
		return err
	}
	defer db2.Close()
	db3, err := pebble.New(path.Join(flags.HomeDir(), "state.slim"), 4096, 1024, "", true)
	if err != nil {
		return err
	}
	defer db3.Close()

	config := params.MainnetChainConfig
	engine, err := ethconfig.CreateConsensusEngine(config, db2)
	if err != nil {
		return err
	}
	chain, err := core.NewHeaderChain(db2, config, engine, func() bool { return false })
	if err != nil {
		return err
	}

	var wg errgroup.Group
	var cur atomic.Uint64
	cur.Store(start + 1)

	sdb := state.NewSlimDatabase(db3)
	for height := start + 1; height <= end; height++ {
		block := rawdb.ReadBlock(db2, rawdb.ReadCanonicalHash(db2, height), height)
		ts := time.Now()
		switch mode {
		case 0:
			if err = generateGeth(chain, sdb, block); err != nil {
				return err
			}
		case 1:
			conflicts, err := generateTxDAG(chain, sdb, block)
			if err != nil {
				return err
			}
			if _, err = rlp.EncodeToBytes(conflicts); err != nil {
				return err
			}
		default:
			conflicts, preloadSlots, err := generateTapes(chain, sdb, block, mode, false)
			if err != nil {
				return err
			}
			if _, err = rlp.EncodeToBytes(conflicts); err != nil {
				return err
			}
			if _, err = rlp.EncodeToBytes(preloadSlots); err != nil {
				return err
			}
		}
		latency := time.Since(ts)
		if _, err = fmt.Fprintln(writer, latency.Nanoseconds()); err != nil {
			return err
		}
		fmt.Printf("\r%d", height)
	}
	if err = wg.Wait(); err != nil {
		return err
	}
	fmt.Println()

	return nil
}

func generateAll(ctx *cli.Context) error {
	if ctx.NArg() != 3 {
		return nil
	}
	start, err := strconv.ParseUint(ctx.Args().Get(0), 10, 64)
	if err != nil {
		return err
	}
	end, err := strconv.ParseUint(ctx.Args().Get(1), 10, 64)
	if err != nil {
		return err
	}

	dataDir := ctx.String(utils.DataDirFlag.Name)
	db1, err := pebble.New(path.Join(dataDir, "geth", "chaindata"), 4096, 1024, "", true)
	if err != nil {
		return err
	}
	db2, err := rawdb.Open(db1, rawdb.OpenOptions{
		Ancient:  path.Join(dataDir, "geth", "chaindata", "ancient"),
		ReadOnly: true,
	})
	if err != nil {
		return err
	}
	defer db2.Close()
	db3, err := pebble.New(path.Join(flags.HomeDir(), "state.slim"), 4096, 1024, "", true)
	if err != nil {
		return err
	}
	defer db3.Close()
	db4, err := pebble.New("depes", 4096, 1024, "", false)
	if err != nil {
		return err
	}
	defer db4.Close()

	config := params.MainnetChainConfig
	engine, err := ethconfig.CreateConsensusEngine(config, db2)
	if err != nil {
		return err
	}
	chain, err := core.NewHeaderChain(db2, config, engine, func() bool { return false })
	if err != nil {
		return err
	}

	var wg errgroup.Group
	var cur atomic.Uint64
	cur.Store(start + 1)
	for range runtime.NumCPU() {
		wg.Go(func() error {
			sdb := state.NewSlimDatabase(db3)
			for height := cur.Add(1) - 1; height <= end; height = cur.Add(1) - 1 {
				block := rawdb.ReadBlock(db2, rawdb.ReadCanonicalHash(db2, height), height)
				txDagConflicts, err := generateTxDAG(chain, sdb, block)
				if err != nil {
					return err
				}
				tapeConflicts, preloadSlots, err := generateTapes(chain, sdb, block, 3, false)
				if err != nil {
					return err
				}

				txDagConflictsBytes, err := rlp.EncodeToBytes(txDagConflicts)
				if err != nil {
					return err
				}
				if err = db4.Put(binary.BigEndian.AppendUint64([]byte{0}, height), txDagConflictsBytes); err != nil {
					return err
				}

				tapeConflictsBytes, err := rlp.EncodeToBytes(tapeConflicts)
				if err != nil {
					return err
				}
				if err = db4.Put(binary.BigEndian.AppendUint64([]byte{1}, height), tapeConflictsBytes); err != nil {
					return err
				}

				preloadSlotsBytes, err := rlp.EncodeToBytes(preloadSlots)
				if err != nil {
					return err
				}
				if err = db4.Put(binary.BigEndian.AppendUint64([]byte{2}, height), preloadSlotsBytes); err != nil {
					return err
				}

				fmt.Printf("\r%d", height)
			}
			return nil
		})
	}
	if err = wg.Wait(); err != nil {
		return err
	}
	fmt.Println()

	if err = os.WriteFile(ctx.Args().Get(2), []byte(strconv.Itoa(int(ReExecCount.Load()))), 0644); err != nil {
		return err
	}

	return db4.Compact(nil, nil)
}

func generateBlockInfo(ctx *cli.Context) error {
	dataDir := ctx.String(utils.DataDirFlag.Name)
	db1, err := pebble.New(path.Join(dataDir, "geth", "chaindata"), 4096, 1024, "", true)
	if err != nil {
		return err
	}
	db2, err := rawdb.Open(db1, rawdb.OpenOptions{
		Ancient:  path.Join(dataDir, "geth", "chaindata", "ancient"),
		ReadOnly: true,
	})
	if err != nil {
		return err
	}
	defer db2.Close()
	db3, err := pebble.New("depes", 4096, 1024, "", true)
	if err != nil {
		return err
	}
	defer db3.Close()
	db4, err := pebble.New("gas-used", 4096, 1024, "", true)
	if err != nil {
		return err
	}
	defer db4.Close()
	file, err := os.Create("result/block-info.csv")
	if err != nil {
		return err
	}
	defer file.Close()
	writer := bufio.NewWriter(file)
	defer writer.Flush()

	for i := range 10 {
		for j := range 10000 {
			height := uint64(23000001 + i*100000 + j)
			block := rawdb.ReadBlock(db2, rawdb.ReadCanonicalHash(db2, height), height)
			gasUsedListBytes, err := db4.Get(binary.BigEndian.AppendUint64(nil, height))
			if err != nil {
				return err
			}
			var gasUsedList []uint64
			if err = rlp.DecodeBytes(gasUsedListBytes, &gasUsedList); err != nil {
				return err
			}
			var gas0 uint64
			for _, gas := range gasUsedList {
				gas0 += gas
			}
			var conflicts1 types.Conflicts
			conflicts1Bytes, err := db3.Get(binary.BigEndian.AppendUint64([]byte{byte(0)}, height))
			if err != nil {
				return err
			}
			if err := rlp.DecodeBytes(conflicts1Bytes, &conflicts1); err != nil {
				return err
			}
			path1 := conflicts1.MaxWeightPath(gasUsedList)
			var gas1 uint64
			for _, k := range path1 {
				gas1 += gasUsedList[k]
			}
			var conflicts2 types.Conflicts
			conflicts2Bytes, err := db3.Get(binary.BigEndian.AppendUint64([]byte{byte(1)}, height))
			if err != nil {
				return err
			}
			if err := rlp.DecodeBytes(conflicts2Bytes, &conflicts2); err != nil {
				return err
			}
			path2 := conflicts2.MaxWeightPath(gasUsedList)
			var gas2 uint64
			for _, k := range path2 {
				gas2 += gasUsedList[k]
			}
			if _, err := fmt.Fprintf(writer, "%d,%d,%d,%d\n", block.Transactions().Len(), gas0, gas1, gas2); err != nil {
				return err
			}
			fmt.Printf("\r%d", height)
		}
	}
	fmt.Println()

	return nil
}

func analyzeMetadata(ctx *cli.Context) error {
	dataDir := ctx.String(utils.DataDirFlag.Name)
	db1, err := pebble.New(path.Join(dataDir, "geth", "chaindata"), 4096, 1024, "", true)
	if err != nil {
		return err
	}
	db2, err := rawdb.Open(db1, rawdb.OpenOptions{
		Ancient:  path.Join(dataDir, "geth", "chaindata", "ancient"),
		ReadOnly: true,
	})
	if err != nil {
		return err
	}
	defer db2.Close()
	db3, err := pebble.New("depes", 4096, 1024, "", true)
	if err != nil {
		return err
	}
	defer db3.Close()
	db4, err := pebble.New("gas-used", 4096, 1024, "", true)
	if err != nil {
		return err
	}
	defer db4.Close()
	file, err := os.Create("result/metadata.csv")
	if err != nil {
		return err
	}
	defer file.Close()
	writer := bufio.NewWriter(file)
	defer writer.Flush()

	for i := range 10 {
		for j := range 10000 {
			height := uint64(23000001 + i*100000 + j)
			block := rawdb.ReadBlock(db2, rawdb.ReadCanonicalHash(db2, height), height)
			var conflicts1 types.Conflicts
			conflicts1Bytes, err := db3.Get(binary.BigEndian.AppendUint64([]byte{byte(0)}, height))
			if err != nil {
				return err
			}
			if err = rlp.DecodeBytes(conflicts1Bytes, &conflicts1); err != nil {
				return err
			}
			var conflicts2 types.Conflicts
			conflicts2Bytes, err := db3.Get(binary.BigEndian.AppendUint64([]byte{byte(1)}, height))
			if err != nil {
				return err
			}
			if err := rlp.DecodeBytes(conflicts2Bytes, &conflicts2); err != nil {
				return err
			}
			preloadSlotsBytes, err := db3.Get(binary.BigEndian.AppendUint64([]byte{byte(2)}, height))
			if err != nil {
				return err
			}
			gasUsedListBytes, err := db4.Get(binary.BigEndian.AppendUint64(nil, height))
			if err != nil {
				return err
			}
			var gasUsedList []uint64
			if err = rlp.DecodeBytes(gasUsedListBytes, &gasUsedList); err != nil {
				return err
			}
			path1 := conflicts1.MaxWeightPath(gasUsedList)
			var gas1 uint64
			for _, k := range path1 {
				gas1 += gasUsedList[k]
			}
			path2 := conflicts2.MaxWeightPath(gasUsedList)
			var gas2 uint64
			for _, k := range path2 {
				gas2 += gasUsedList[k]
			}
			if _, err := fmt.Fprintf(writer, "%d,%d,%d,%d,%d,%d,%d,%d\n", block.Size(),
				len(conflicts1Bytes), len(conflicts2Bytes), len(preloadSlotsBytes),
				conflicts1.DepNum(), conflicts2.DepNum(), gas1, gas2); err != nil {
				return err
			}
			fmt.Printf("\r%d", height)
		}
	}
	fmt.Println()

	return nil
}

func readBlocks(db ethdb.Reader, tapeName string, mode int, start, end uint64) (map[uint64]*types.Block, error) {
	tapeDb, err := pebble.New(tapeName, 4096, 1024, "", true)
	if err != nil {
		return nil, err
	}
	defer tapeDb.Close()

	var wg errgroup.Group
	var mu sync.Mutex
	blocks := make(map[uint64]*types.Block)
	var cur atomic.Uint64
	cur.Store(start + 1)
	for range runtime.NumCPU() {
		wg.Go(func() error {
			for height := cur.Add(1) - 1; height <= end; height = cur.Add(1) - 1 {
				hash := rawdb.ReadCanonicalHash(db, height)
				block := rawdb.ReadBlock(db, hash, height)

				if mode > 0 {
					conflictsBytes, err := tapeDb.Get(binary.BigEndian.AppendUint64([]byte{byte(min(mode-1, 1))}, height))
					if err != nil {
						return err
					}
					var conflicts types.Conflicts
					err = rlp.DecodeBytes(conflictsBytes, &conflicts)
					if err != nil {
						return err
					}
					block.SetConflicts(&conflicts)
				}

				preloadSlotsBytes, err := tapeDb.Get(binary.BigEndian.AppendUint64([]byte{2}, height))
				if err != nil {
					return err
				}
				var preloadSlots types.PreloadSlots
				err = rlp.DecodeBytes(preloadSlotsBytes, &preloadSlots)
				if err != nil {
					return err
				}
				block.SetPreloadSlots(&preloadSlots)

				mu.Lock()
				blocks[height] = block
				mu.Unlock()
			}
			return nil
		})
	}
	if err := wg.Wait(); err != nil {
		return nil, err
	}
	return blocks, nil
}

func run(ctx *cli.Context) error {
	if ctx.NArg() != 4 {
		return nil
	}
	mode, err := strconv.Atoi(ctx.Args().Get(0))
	if err != nil {
		return err
	}
	start, err := strconv.ParseUint(ctx.Args().Get(1), 10, 64)
	if err != nil {
		return err
	}
	end, err := strconv.ParseUint(ctx.Args().Get(2), 10, 64)
	if err != nil {
		return err
	}
	file, err := os.Create(ctx.Args().Get(3))
	if err != nil {
		return err
	}
	defer file.Close()
	writer := bufio.NewWriter(file)
	defer writer.Flush()

	dataDir := ctx.String(utils.DataDirFlag.Name)
	db1, err := pebble.New(path.Join(dataDir, "geth", "chaindata"), 4096, 1024, "", true)
	if err != nil {
		return err
	}
	db2, err := rawdb.Open(db1, rawdb.OpenOptions{
		Ancient:  path.Join(dataDir, "geth", "chaindata", "ancient"),
		ReadOnly: true,
	})
	if err != nil {
		return err
	}
	defer db2.Close()
	db3, err := pebble.New(path.Join(flags.HomeDir(), "state.slim"), 4096, 1024, "", true)
	if err != nil {
		return err
	}
	defer db3.Close()

	blocks, err := readBlocks(db2, "depes", mode, start, end)
	if err != nil {
		return err
	}

	config := params.MainnetChainConfig
	engine, err := ethconfig.CreateConsensusEngine(config, db2)
	if err != nil {
		return err
	}
	chain, err := core.NewHeaderChain(db2, config, engine, func() bool { return false })
	if err != nil {
		return err
	}
	processor := core.NewStateProcessor(chain)
	processor.SetMode(mode)

	sdb := state.NewSlimDatabase(db3)

	for height := start + 1; height <= end; height++ {
		block := blocks[height]
		statedb, err := state.NewSlim(sdb, height-1)
		if err != nil {
			return err
		}
		ts := time.Now()
		if _, err = processor.Process(block, statedb, vm.Config{}); err != nil {
			return err
		}
		latency := time.Since(ts)
		if _, err = fmt.Fprintln(writer, latency.Nanoseconds()); err != nil {
			return err
		}

		fmt.Printf("\r%d", height)
	}
	fmt.Println()

	return nil
}

func adjustPrivateTxs(height uint64, privates *bitset.BitSet, rate uint) {
	if rate > 100 {
		return
	}
	txNum := privates.Len()
	target := txNum * rate / 100
	current := privates.Count()
	if current == target {
		return
	}
	random := rand.New(rand.NewSource(int64(height)))
	for _, tx := range random.Perm(int(txNum)) {
		private := privates.Test(uint(tx))
		if current > target && private {
			privates.Clear(uint(tx))
			current--
		} else if current < target && !private {
			privates.Set(uint(tx))
			current++
		}
		if current == target {
			break
		}
	}
}

func readPrivateTxs(blocks map[uint64]*types.Block, start, end uint64, rate int) (map[uint64]*bitset.BitSet, error) {
	db, err := pebble.New(path.Join(flags.HomeDir(), "private-txs"), 0, 0, "", true)
	if err != nil {
		return nil, err
	}
	defer db.Close()

	var wg errgroup.Group
	var mu sync.Mutex
	privatesMap := make(map[uint64]*bitset.BitSet)
	var cur atomic.Uint64
	cur.Store(start + 1)
	for range runtime.NumCPU() {
		wg.Go(func() error {
			for height := cur.Add(1) - 1; height <= end; height = cur.Add(1) - 1 {
				block := blocks[height]
				txNum := block.Transactions().Len()
				privates := bitset.New(uint(txNum))
				privateTxsBytes, err := db.Get(binary.BigEndian.AppendUint64(nil, height))
				if err != nil {
					return err
				}
				var txs []uint
				if err = rlp.DecodeBytes(privateTxsBytes, &txs); err != nil {
					return err
				}
				for _, tx := range txs {
					privates.Set(tx)
				}
				adjustPrivateTxs(height, privates, uint(rate))
				mu.Lock()
				privatesMap[height] = privates
				mu.Unlock()
			}
			return nil
		})
	}
	if err := wg.Wait(); err != nil {
		return nil, err
	}
	return privatesMap, nil
}

func runSeer(ctx *cli.Context) error {
	if ctx.NArg() != 5 {
		return fmt.Errorf("got %d arguments, expected 5", ctx.NArg())
	}
	mode, err := strconv.Atoi(ctx.Args().Get(0))
	if err != nil {
		return err
	}
	privateRate, err := strconv.Atoi(ctx.Args().Get(1))
	if err != nil {
		return err
	}
	start, err := strconv.ParseUint(ctx.Args().Get(2), 10, 64)
	if err != nil {
		return err
	}
	end, err := strconv.ParseUint(ctx.Args().Get(3), 10, 64)
	if err != nil {
		return err
	}
	file, err := os.Create(ctx.Args().Get(4))
	if err != nil {
		return err
	}
	defer file.Close()
	writer := bufio.NewWriter(file)
	defer writer.Flush()

	dataDir := ctx.String(utils.DataDirFlag.Name)
	db1, err := pebble.New(path.Join(dataDir, "geth", "chaindata"), 4096, 1024, "", true)
	if err != nil {
		return err
	}
	db2, err := rawdb.Open(db1, rawdb.OpenOptions{
		Ancient:  path.Join(dataDir, "geth", "chaindata", "ancient"),
		ReadOnly: true,
	})
	if err != nil {
		return err
	}
	defer db2.Close()
	db3, err := pebble.New(path.Join(flags.HomeDir(), "state.slim"), 4096, 1024, "", true)
	if err != nil {
		return err
	}
	defer db3.Close()

	blocks, err := readBlocks(db2, "depes", mode, start, end)
	if err != nil {
		return err
	}

	privatesMap, err := readPrivateTxs(blocks, start, end, privateRate)
	if err != nil {
		return err
	}

	config := params.MainnetChainConfig
	engine, err := ethconfig.CreateConsensusEngine(config, db2)
	if err != nil {
		return err
	}
	chain, err := core.NewHeaderChain(db2, config, engine, func() bool { return false })
	if err != nil {
		return err
	}
	processor := core.NewStateProcessor(chain)

	sdb := state.NewSlimDatabase(db3)

	branchTable := vm.CreateNewTable()

	for height := start + 1; height <= end; height++ {
		block := blocks[height]

		mvCache := vm.NewMVCache()
		preTable := vm.NewPreExecutionTable()
		statedb1, err := state.NewSlim(sdb, height-1)
		if err != nil {
			return err
		}
		processor.SetMode(0)
		if _, err = processor.Process(block, statedb1, vm.Config{
			IsSeer:            true,
			IsPreExec:         true,
			MVCache:           mvCache,
			VarTable:          branchTable,
			PreExecutionTable: preTable,
			Privates:          privatesMap[height],
		}); err != nil {
			return err
		}

		statedb2, err := state.NewSlim(sdb, height-1)
		if err != nil {
			return err
		}
		processor.SetMode(mode)
		ts := time.Now()
		if _, err = processor.Process(block, statedb2, vm.Config{
			IsSeer:            true,
			MVCache:           mvCache,
			VarTable:          branchTable,
			PreExecutionTable: preTable,
			Privates:          privatesMap[height],
		}); err != nil {
			return err
		}
		latency := time.Since(ts)
		if _, err = fmt.Fprintln(writer, latency.Nanoseconds()); err != nil {
			return err
		}
		branchTable.Sweep()

		fmt.Printf("\r%d", height)
	}
	fmt.Println()

	return nil
}

func verify(ctx *cli.Context) error {
	if ctx.NArg() != 2 {
		return fmt.Errorf("got %d arguments, expected 2", ctx.NArg())
	}
	start, err := strconv.ParseUint(ctx.Args().Get(0), 10, 64)
	if err != nil {
		return err
	}
	end, err := strconv.ParseUint(ctx.Args().Get(1), 10, 64)
	if err != nil {
		return err
	}

	dataDir := ctx.String(utils.DataDirFlag.Name)
	db1, err := pebble.New(path.Join(dataDir, "geth", "chaindata"), 4096, 1024, "", true)
	if err != nil {
		return err
	}
	db2, err := rawdb.Open(db1, rawdb.OpenOptions{
		Ancient:  path.Join(dataDir, "geth", "chaindata", "ancient"),
		ReadOnly: true,
	})
	if err != nil {
		return err
	}
	defer db2.Close()
	db3, err := pebble.New(path.Join(flags.HomeDir(), "state.slim"), 4096, 1024, "", true)
	if err != nil {
		return err
	}
	defer db3.Close()

	config := params.MainnetChainConfig
	engine, err := ethconfig.CreateConsensusEngine(config, db2)
	if err != nil {
		return err
	}
	chain, err := core.NewHeaderChain(db2, config, engine, func() bool { return false })
	if err != nil {
		return err
	}
	processor := core.NewStateProcessor(chain)
	processor.SetMode(3)

	var wg errgroup.Group
	var cur atomic.Uint64
	cur.Store(start + 1)
	for range runtime.NumCPU() {
		wg.Go(func() error {
			sdb := state.NewSlimDatabase(db3)
			for height := cur.Add(1) - 1; height <= end; height = cur.Add(1) - 1 {
				block := rawdb.ReadBlock(db2, rawdb.ReadCanonicalHash(db2, height), height)
				statedb1, conflicts, preloadSlots, err := generateTapesHelper(chain, sdb, block, 3, false)
				if err != nil {
					return err
				}
				block.SetConflicts(conflicts)
				block.SetPreloadSlots(preloadSlots)

				statedb2, err := state.NewSlim(sdb, height-1)
				if err != nil {
					return err
				}
				if _, err = processor.Process(block, statedb2, vm.Config{}); err != nil {
					return err
				}
				accounts1, storages1, codes1 := statedb1.PendingStates(config.IsEIP158(block.Number()))
				accounts2, storages2, codes2 := statedb2.PendingStates(config.IsEIP158(block.Number()))
				if !maps.EqualFunc(accounts1, accounts2, func(a, b *types.StateAccount) bool {
					if a == b {
						return true
					}
					if a == nil || b == nil {
						return false
					}
					return a.Nonce == b.Nonce && a.Balance.Eq(b.Balance) && bytes.Equal(a.CodeHash, b.CodeHash)
				}) || !maps.Equal(storages1, storages2) || !maps.EqualFunc(codes1, codes2, bytes.Equal) {
					fmt.Println("mismatch", height)
				}
				fmt.Printf("\r%d", height)
			}
			return nil
		})
	}
	if err = wg.Wait(); err != nil {
		return err
	}
	fmt.Println()

	return nil
}

func main() {
	if err := app.Run(os.Args); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
}
