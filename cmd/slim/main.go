package main

import (
	"encoding/binary"
	"encoding/json"
	"fmt"
	"math/big"
	"net/http"
	_ "net/http/pprof"
	"os"
	"os/signal"
	"path"
	"runtime"
	"slices"
	"strconv"
	"sync/atomic"
	"syscall"

	mapset "github.com/deckarep/golang-set/v2"
	"github.com/ethereum/go-ethereum/cmd/utils"
	"github.com/ethereum/go-ethereum/common"
	"github.com/ethereum/go-ethereum/core"
	"github.com/ethereum/go-ethereum/core/rawdb"
	"github.com/ethereum/go-ethereum/core/state"
	"github.com/ethereum/go-ethereum/core/tracing"
	"github.com/ethereum/go-ethereum/core/types"
	"github.com/ethereum/go-ethereum/core/vm"
	"github.com/ethereum/go-ethereum/crypto"
	"github.com/ethereum/go-ethereum/eth/ethconfig"
	"github.com/ethereum/go-ethereum/ethdb/pebble"
	"github.com/ethereum/go-ethereum/internal/flags"
	"github.com/ethereum/go-ethereum/params"
	"github.com/ethereum/go-ethereum/rlp"
	"github.com/ethereum/go-ethereum/strie"
	"github.com/holiman/uint256"
	"github.com/urfave/cli/v2"
	"golang.org/x/sync/errgroup"
)

var app = cli.NewApp()

func init() {
	app.Commands = []*cli.Command{
		{
			Name:   "execute",
			Action: execute,
			Flags: []cli.Flag{
				utils.DataDirFlag,
			},
		},
		{
			Name: "verify",
			Flags: []cli.Flag{
				utils.DataDirFlag,
			},
			Action: verify,
		},
		{
			Name: "show",
			Flags: []cli.Flag{
				utils.DataDirFlag,
			},
			Action: show,
		},
		{
			Name: "reset",
			Flags: []cli.Flag{
				utils.DataDirFlag,
			},
			Action: reset,
		},
		{
			Name:   "compact",
			Action: compact,
		},
		{
			Name: "extract-blocks",
			Flags: []cli.Flag{
				utils.DataDirFlag,
			},
			Action: extractBlocks,
		},
		{
			Name: "extract-states",
			Flags: []cli.Flag{
				utils.DataDirFlag,
			},
			Action: extractStates,
		},
	}
}

func execute(ctx *cli.Context) error {
	sigChan := make(chan os.Signal, 1)
	signal.Notify(sigChan, os.Interrupt, syscall.SIGTERM)

	target, err := strconv.ParseUint(ctx.Args().Get(0), 10, 64)
	if err != nil {
		panic(err)
	}

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
	db3, err := pebble.New(path.Join(flags.HomeDir(), "state.slim"), 8192, 1024, "", false)
	if err != nil {
		return err
	}
	defer db3.Close()

	config := params.MainnetChainConfig
	sdb := state.NewSlimDatabase(db3)

	var start uint64
	startBytes, err := db3.Get([]byte("CURRENT"))
	if err == nil {
		start = binary.BigEndian.Uint64(startBytes) + 1
	} else {
		block := rawdb.ReadBlock(db2, rawdb.ReadCanonicalHash(db2, 0), 0)
		statedb, err := state.NewSlim(sdb, 0)
		if err != nil {
			return err
		}
		for addr, account := range core.DefaultGenesisBlock().Alloc {
			if account.Balance != nil {
				statedb.SetBalance(addr, uint256.MustFromBig(account.Balance), tracing.BalanceIncreaseGenesisBalance)
			}
		}
		if _, err = statedb.Commit(block.NumberU64(), config.IsEIP158(block.Number()), config.IsCancun(block.Number(), block.Time())); err != nil {
			return err
		}
		if err = sdb.Commit(0); err != nil {
			return err
		}
		if err = db3.Put([]byte("CURRENT"), binary.BigEndian.AppendUint64(nil, 0)); err != nil {
			return err
		}
		start = 1
	}

	engine, err := ethconfig.CreateConsensusEngine(config, db2)
	if err != nil {
		return err
	}
	chain, err := core.NewHeaderChain(db2, config, engine, func() bool {
		return false
	})
	if err != nil {
		return err
	}
	processor := core.NewStateProcessor(chain)

loop:
	for height := start; height <= target; height++ {
		if height%1000 == 0 {
			fmt.Println(height)
		}
		block := rawdb.ReadBlock(db2, rawdb.ReadCanonicalHash(db2, height), height)
		statedb, err := state.NewSlim(sdb, height-1)
		if err != nil {
			return err
		}
		if _, err = processor.Process(block, statedb, vm.Config{}); err != nil {
			return err
		}
		if _, err = statedb.Commit(block.NumberU64(), config.IsEIP158(block.Number()), config.IsCancun(block.Number(), block.Time())); err != nil {
			return err
		}
		if err = sdb.Commit(height); err != nil {
			return err
		}
		if err = db3.Put([]byte("CURRENT"), binary.BigEndian.AppendUint64(nil, height)); err != nil {
			return err
		}
		select {
		case <-sigChan:
			break loop
		default:
		}
	}

	return db3.Compact(nil, nil)
}

func readBlock(ctx *cli.Context, height uint64) (*types.Block, error) {
	dataDir := ctx.String(utils.DataDirFlag.Name)
	db1, err := pebble.New(path.Join(dataDir, "geth", "chaindata"), 0, 0, "", true)
	if err != nil {
		return nil, err
	}
	db2, err := rawdb.Open(db1, rawdb.OpenOptions{
		Ancient:  path.Join(dataDir, "geth", "chaindata", "ancient"),
		ReadOnly: true,
	})
	if err != nil {
		return nil, err
	}
	return rawdb.ReadBlock(db2, rawdb.ReadCanonicalHash(db2, height), height), nil
}

func verify(ctx *cli.Context) error {
	target, err := strconv.ParseUint(ctx.Args().Get(0), 10, 64)
	if err != nil {
		panic(err)
	}
	block, err := readBlock(ctx, target)
	if err != nil {
		panic(err)
	}

	db1, err := pebble.New(path.Join(flags.HomeDir(), "state.slim"), 0, 0, "", true)
	if err != nil {
		panic(err)
	}
	defer db1.Close()
	if err = os.RemoveAll(path.Join(flags.HomeDir(), "state.tmp1")); err != nil {
		panic(err)
	}
	db2, err := pebble.New(path.Join(flags.HomeDir(), "state.tmp1"), 4096, 1024, "", false)
	if err != nil {
		panic(err)
	}
	defer func() {
		db2.Close()
		os.RemoveAll(path.Join(flags.HomeDir(), "state.tmp1"))
	}()
	if err = os.RemoveAll(path.Join(flags.HomeDir(), "state.tmp2")); err != nil {
		panic(err)
	}
	db3, err := pebble.New(path.Join(flags.HomeDir(), "state.tmp2"), 4096, 1024, "", false)
	if err != nil {
		panic(err)
	}
	defer func() {
		db3.Close()
		os.RemoveAll(path.Join(flags.HomeDir(), "state.tmp2"))
	}()

	deletes := make(map[common.Address]uint64)
	{
		iter := db1.NewIterator(rawdb.DeletePrefix, nil)
		for iter.Next() {
			addr := common.BytesToAddress(iter.Key()[len(rawdb.DeletePrefix) : len(rawdb.DeletePrefix)+common.AddressLength])
			height := binary.BigEndian.Uint64(iter.Key()[len(rawdb.DeletePrefix)+common.AddressLength:])
			if height > target {
				continue
			}
			deletes[addr] = height
			if len(deletes)%100000 == 0 {
				fmt.Println("deletes ", addr)
			}
		}
		iter.Release()
	}

	var wg errgroup.Group
	wg.SetLimit(runtime.NumCPU())

	for i := 0; i < 256; i++ {
		wg.Go(func() error {
			count := 0
			var currAddr *common.Address
			var currValue []byte
			iter := db1.NewIterator(append(rawdb.AccountPrefix, byte(i)), nil)
			for iter.Next() {
				addr := common.BytesToAddress(iter.Key()[len(rawdb.AccountPrefix) : len(rawdb.AccountPrefix)+common.AddressLength])
				height := binary.BigEndian.Uint64(iter.Key()[len(rawdb.AccountPrefix)+common.AddressLength:])
				if height < deletes[addr] || height > target {
					continue
				}
				if currAddr != nil && *currAddr != addr {
					if err := db2.Put(currAddr.Bytes(), currValue); err != nil {
						panic(err)
					}
					count++
					if count%10000 == 0 {
						fmt.Println("accounts", *currAddr)
					}
				}
				currAddr = &addr
				if value := iter.Value(); len(value) != 0 {
					currValue = slices.Clone(value)
				} else {
					currValue = nil
				}
			}
			iter.Release()
			if currAddr != nil {
				if err := db2.Put(currAddr.Bytes(), currValue); err != nil {
					panic(err)
				}
				count++
				if count%10000 == 0 {
					fmt.Println("accounts", *currAddr)
				}
			}
			return nil
		})
	}
	if err = wg.Wait(); err != nil {
		panic(err)
	}

	for i := 0; i < 256; i++ {
		wg.Go(func() error {
			count := 0
			var currAddr *common.Address
			var currKey *common.Hash
			var currValue []byte
			storageTrie := strie.NewEmpty(false)
			iter := db1.NewIterator(append(rawdb.StoragePrefix, byte(i)), nil)
			for iter.Next() {
				addr := common.BytesToAddress(iter.Key()[len(rawdb.StoragePrefix) : len(rawdb.StoragePrefix)+common.AddressLength])
				key := common.BytesToHash(iter.Key()[len(rawdb.StoragePrefix)+common.AddressLength : len(rawdb.StoragePrefix)+common.AddressLength+common.HashLength])
				height := binary.BigEndian.Uint64(iter.Key()[len(rawdb.StoragePrefix)+common.AddressLength+common.HashLength:])
				if height < deletes[addr] || height > target {
					continue
				}
				if currAddr != nil && currKey != nil && (*currAddr != addr || *currKey != key) && currValue != nil {
					hk := crypto.Keccak256((*currKey)[:])
					v, _ := rlp.EncodeToBytes(currValue)
					if err = storageTrie.Update(hk, v); err != nil {
						panic(err)
					}
				}
				if currAddr != nil && *currAddr != addr {
					accountBytes, err := db2.Get(currAddr.Bytes())
					if err != nil {
						panic(err)
					}
					account, err := types.FullAccount(accountBytes)
					if err != nil {
						panic(err)
					}
					account.Root = storageTrie.Hash()
					accountBytes = types.SlimAccountRLP(*account)
					if err := db2.Put(currAddr.Bytes(), accountBytes); err != nil {
						panic(err)
					}
					count++
					if count%10000 == 0 {
						fmt.Println("storages", *currAddr)
					}
					currKey = nil
					storageTrie = strie.NewEmpty(false)
				}
				currAddr = &addr
				currKey = &key
				if value := iter.Value(); len(value) != 0 {
					currValue = slices.Clone(value)
				} else {
					currValue = nil
				}
			}
			iter.Release()
			if currKey != nil && currValue != nil {
				hk := crypto.Keccak256((*currKey)[:])
				v, _ := rlp.EncodeToBytes(currValue)
				if err = storageTrie.Update(hk, v); err != nil {
					panic(err)
				}
			}
			if currAddr != nil {
				accountBytes, err := db2.Get(currAddr.Bytes())
				if err != nil {
					panic(err)
				}
				account, err := types.FullAccount(accountBytes)
				if err != nil {
					panic(err)
				}
				account.Root = storageTrie.Hash()
				accountBytes = types.SlimAccountRLP(*account)
				if err := db2.Put(currAddr.Bytes(), accountBytes); err != nil {
					panic(err)
				}
				count++
				if count%10000 == 0 {
					fmt.Println("storages", *currAddr)
				}
			}
			return nil
		})
	}
	if err = wg.Wait(); err != nil {
		panic(err)
	}

	for i := 0; i < 256; i++ {
		wg.Go(func() error {
			iter := db2.NewIterator([]byte{byte(i)}, nil)
			defer iter.Release()
			for iter.Next() {
				addrHash := crypto.Keccak256Hash(iter.Key())
				account, err := types.FullAccount(iter.Value())
				if err != nil {
					panic(err)
				}
				data, err := rlp.EncodeToBytes(account)
				if err != nil {
					panic(err)
				}
				if err := db3.Put(addrHash.Bytes(), data); err != nil {
					panic(err)
				}
			}
			return nil
		})
	}
	if err = wg.Wait(); err != nil {
		panic(err)
	}
	db2.Close()

	accountTrie := strie.NewEmpty(true)
	iter := db3.NewIterator(nil, nil)
	for iter.Next() {
		if err = accountTrie.Update(iter.Key(), slices.Clone(iter.Value())); err != nil {
			panic(err)
		}
	}
	root := accountTrie.Hash()
	if root != block.Root() {
		panic(fmt.Sprintf("root not match: %s != %s", root, block.Root()))
	}
	fmt.Println(root, block.Root())

	return nil
}

func show(ctx *cli.Context) error {
	target, err := strconv.ParseUint(ctx.Args().Get(0), 10, 64)
	if err != nil {
		return err
	}

	db1, err := pebble.New(path.Join(flags.HomeDir(), "state.slim"), 0, 0, "", true)
	if err != nil {
		return err
	}
	defer db1.Close()

	iter := db1.NewIterator(rawdb.DeletePrefix, nil)
	for iter.Next() {
		height := binary.BigEndian.Uint64(iter.Key()[len(rawdb.DeletePrefix)+common.AddressLength:])
		if height == target {
			addr := common.BytesToAddress(iter.Key()[len(rawdb.DeletePrefix) : len(rawdb.DeletePrefix)+common.AddressLength])
			fmt.Println("delete ", addr)
		}
	}
	iter.Release()

	iter = db1.NewIterator(rawdb.AccountPrefix, nil)
	for iter.Next() {
		height := binary.BigEndian.Uint64(iter.Key()[len(rawdb.AccountPrefix)+common.AddressLength:])
		if height == target {
			addr := common.BytesToAddress(iter.Key()[len(rawdb.AccountPrefix) : len(rawdb.AccountPrefix)+common.AddressLength])
			account, _ := types.FullAccount(iter.Value())
			accountJson, err := json.Marshal(struct {
				Nonce   uint64
				Balance *uint256.Int
			}{account.Nonce, account.Balance})
			if err != nil {
				panic(err)
			}
			fmt.Println("account", addr, string(accountJson))
		}
	}
	iter.Release()

	iter = db1.NewIterator(rawdb.StoragePrefix, nil)
	for iter.Next() {
		height := binary.BigEndian.Uint64(iter.Key()[len(rawdb.StoragePrefix)+common.AddressLength+common.HashLength:])
		if height == target {
			addr := common.BytesToAddress(iter.Key()[len(rawdb.StoragePrefix) : len(rawdb.StoragePrefix)+common.AddressLength])
			key := common.BytesToHash(iter.Key()[len(rawdb.StoragePrefix)+common.AddressLength : len(rawdb.StoragePrefix)+common.AddressLength+common.HashLength])
			fmt.Println("storage", addr, key, common.BytesToHash(iter.Value()))
		}
	}
	iter.Release()

	return nil
}

func reset(ctx *cli.Context) error {
	target, err := strconv.ParseUint(ctx.Args().Get(0), 10, 64)
	if err != nil {
		return err
	}

	db, err := pebble.New(path.Join(flags.HomeDir(), "state.slim"), 8192, 1024, "", false)
	if err != nil {
		return err
	}
	defer db.Close()

	deletes := mapset.NewThreadUnsafeSet[string]()
	iter := db.NewIterator(nil, nil)
	defer iter.Release()
	for iter.Next() {
		key := iter.Key()
		if len(key) == 1+common.AddressLength+8 || len(key) == 1+common.AddressLength+common.HashLength+8 {
			height := binary.BigEndian.Uint64(key[len(key)-8:])
			if height > target {
				deletes.Add(string(key))
				fmt.Println(common.Bytes2Hex(key))
			}
		}
	}
	for key := range deletes.Iter() {
		if err = db.Delete([]byte(key)); err != nil {
			return err
		}
	}
	if err = db.Put([]byte("CURRENT"), binary.BigEndian.AppendUint64(nil, target)); err != nil {
		return err
	}
	return db.Compact(nil, nil)
}

func compact(ctx *cli.Context) error {
	if ctx.NArg() != 1 {
		return fmt.Errorf("got %d arguments, expected 1", ctx.NArg())
	}

	db, err := pebble.New(ctx.Args().First(), 16384, 1024, "", false)
	if err != nil {
		return err
	}
	defer db.Close()
	return db.Compact(nil, nil)
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

	var wg errgroup.Group
	ch1 := make(chan uint64, 100000)
	ch2 := make(chan *types.Block, 100000)
	var count atomic.Int64
	wg.Go(func() error {
		for height := uint64(0); height <= end; height++ {
			ch1 <- height
		}
		close(ch1)
		return nil
	})
	count.Store(int64(runtime.NumCPU()))
	for range runtime.NumCPU() {
		wg.Go(func() error {
			for height := range ch1 {
				var block *types.Block
				if height == 0 || height >= start && height <= end {
					hash := rawdb.ReadCanonicalHash(db2, height)
					block = rawdb.ReadBlock(db2, hash, height)
				} else {
					block = types.NewBlockWithHeader(&types.Header{Number: big.NewInt(int64(height))})
				}
				ch2 <- block
			}
			if count.Add(-1) == 0 {
				close(ch2)
			}
			return nil
		})
	}
	blockMap := make(map[uint64]*types.Block)
	var next uint64
	for block := range ch2 {
		blockMap[block.NumberU64()] = block
		var blocks []*types.Block
		var receipts []rlp.RawValue
		for {
			block, ok := blockMap[next]
			if !ok {
				break
			}
			delete(blockMap, next)
			blocks = append(blocks, block)
			receipts = append(receipts, rlp.RawValue{})
			next++
		}
		if len(blocks) != 0 {
			if _, err := rawdb.WriteAncientBlocks(db4, blocks, receipts); err != nil {
				return err
			}
		}
		fmt.Printf("\r%d", block.NumberU64())
	}
	fmt.Println()

	return db4.Compact(nil, nil)
}

func extractStates(ctx *cli.Context) error {
	// TODO not available now
	if ctx.NArg() != 4 {
		return fmt.Errorf("got %d arguments, expected 4", ctx.NArg())
	}
	source := ctx.Args().Get(0)
	target := ctx.Args().Get(1)
	begin, err := strconv.ParseUint(ctx.Args().Get(2), 10, 64)
	if err != nil {
		return err
	}
	end, err := strconv.ParseUint(ctx.Args().Get(3), 10, 64)
	if err != nil {
		return err
	}

	db1, err := pebble.New(source, 8192, 1024, "", true)
	if err != nil {
		return err
	}
	defer db1.Close()
	if err = os.RemoveAll(target); err != nil {
		return err
	}
	db2, err := pebble.New(target, 8192, 1024, "", false)
	if err != nil {
		return err
	}
	defer db2.Close()

	count := 0
	deletes := make(map[common.Address]uint64)
	iter := db1.NewIterator(rawdb.DeletePrefix, nil)
	for iter.Next() {
		addr := common.BytesToAddress(iter.Key()[len(rawdb.DeletePrefix) : len(rawdb.DeletePrefix)+common.AddressLength])
		height := binary.BigEndian.Uint64(iter.Key()[len(rawdb.DeletePrefix)+common.AddressLength:])
		if height < begin {
			deletes[addr] = height
		} else if height <= end {
			if err = db2.Put(slices.Clone(iter.Key()), slices.Clone(iter.Value())); err != nil {
				return err
			}
		}
		count++
		if count%1000000 == 0 {
			fmt.Println("delete", common.Bytes2Hex(iter.Key()))
		}
	}
	iter.Release()

	var wg errgroup.Group
	wg.SetLimit(runtime.NumCPU())
	for i := 0; i < 256; i++ {
		wg.Go(func() error {
			count := 0
			var pendingKey, pendingValue []byte
			iter := db1.NewIterator(append(rawdb.AccountPrefix, byte(i)), nil)
			for iter.Next() {
				addr := common.BytesToAddress(iter.Key()[len(rawdb.AccountPrefix) : len(rawdb.AccountPrefix)+common.AddressLength])
				height := binary.BigEndian.Uint64(iter.Key()[len(rawdb.AccountPrefix)+common.AddressLength:])
				if height < deletes[addr] {
					continue
				}
				if pendingKey != nil && !slices.Equal(pendingKey[:len(rawdb.AccountPrefix)+common.AddressLength], iter.Key()[:len(rawdb.AccountPrefix)+common.AddressLength]) {
					binary.BigEndian.PutUint64(pendingKey[len(rawdb.AccountPrefix)+common.AddressLength:], height)
					if err = db2.Put(pendingKey, pendingValue); err != nil {
						return err
					}
					pendingKey = nil
					pendingValue = nil
				}
				if height < begin {
					pendingKey = slices.Clone(iter.Key())
					pendingValue = slices.Clone(iter.Value())
				} else {
					if pendingKey != nil {
						binary.BigEndian.PutUint64(pendingKey[len(rawdb.AccountPrefix)+common.AddressLength:], begin)
						if err = db2.Put(pendingKey, pendingValue); err != nil {
							return err
						}
						pendingKey = nil
						pendingValue = nil
					}
					if height <= end {
						if err = db2.Put(slices.Clone(iter.Key()), slices.Clone(iter.Value())); err != nil {
							return err
						}
					}
				}
				count++
				if count%1000000 == 0 {
					fmt.Println("account", common.Bytes2Hex(iter.Key()))
				}
			}
			if pendingKey != nil {
				binary.BigEndian.PutUint64(pendingKey[len(rawdb.AccountPrefix)+common.AddressLength:], begin)
				if err = db2.Put(pendingKey, pendingValue); err != nil {
					return err
				}
				pendingKey = nil
				pendingValue = nil
			}
			iter.Release()
			return nil
		})
	}

	for i := 0; i < 256; i++ {
		wg.Go(func() error {
			count := 0
			var pendingKey, pendingValue []byte
			iter := db1.NewIterator(append(rawdb.StoragePrefix, byte(i)), nil)
			for iter.Next() {
				addr := common.BytesToAddress(iter.Key()[len(rawdb.StoragePrefix) : len(rawdb.StoragePrefix)+common.AddressLength])
				height := binary.BigEndian.Uint64(iter.Key()[len(rawdb.StoragePrefix)+common.AddressLength+common.HashLength:])
				if height < deletes[addr] {
					continue
				}
				if pendingKey != nil && !slices.Equal(pendingKey[:len(rawdb.StoragePrefix)+common.AddressLength+common.HashLength], iter.Key()[:len(rawdb.StoragePrefix)+common.AddressLength+common.HashLength]) {
					binary.BigEndian.PutUint64(pendingKey[len(rawdb.StoragePrefix)+common.AddressLength+common.HashLength:], height)
					if err = db2.Put(pendingKey, pendingValue); err != nil {
						return err
					}
					pendingKey = nil
					pendingValue = nil
				}
				if height < begin {
					pendingKey = slices.Clone(iter.Key())
					pendingValue = slices.Clone(iter.Value())
				} else {
					if pendingKey != nil {
						binary.BigEndian.PutUint64(pendingKey[len(rawdb.StoragePrefix)+common.AddressLength+common.HashLength:], height)
						if err = db2.Put(pendingKey, pendingValue); err != nil {
							return err
						}
						pendingKey = nil
						pendingValue = nil
					}
					if height <= end {
						if err = db2.Put(slices.Clone(iter.Key()), slices.Clone(iter.Value())); err != nil {
							return err
						}
					}
				}
				count++
				if count%1000000 == 0 {
					fmt.Println("storage", common.Bytes2Hex(iter.Key()))
				}
			}
			if pendingKey != nil {
				binary.BigEndian.PutUint64(pendingKey[len(rawdb.StoragePrefix)+common.AddressLength+common.HashLength:], begin)
				if err = db2.Put(pendingKey, pendingValue); err != nil {
					return err
				}
				pendingKey = nil
				pendingValue = nil
			}
			iter.Release()
			return nil
		})
	}

	if err = wg.Wait(); err != nil {
		return err
	}

	return db2.Compact(nil, nil)
}

func main() {
	go func() {
		if err := http.ListenAndServe("127.0.0.1:6060", nil); err != nil {
			panic(err)
		}
	}()
	if err := app.Run(os.Args); err != nil {
		fmt.Fprintln(os.Stderr, err)
	}
}
