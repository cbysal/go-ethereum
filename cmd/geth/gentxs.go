package main

import (
	"context"
	"errors"
	"math/rand/v2"

	"github.com/ethereum/go-ethereum/accounts"
	"github.com/ethereum/go-ethereum/common"
	"github.com/ethereum/go-ethereum/common/hexutil"
	"github.com/ethereum/go-ethereum/core/types"
	"github.com/ethereum/go-ethereum/eccb"
	"github.com/ethereum/go-ethereum/internal/ethapi"
	"github.com/ethereum/go-ethereum/log"
	"github.com/urfave/cli/v2"
)

var (
	genTxsCommand = &cli.Command{
		Action:      genTxs,
		Name:        "gentxs",
		Usage:       "Generate transactions",
		Flags:       nodeFlags,
		Description: "subcommand to generate transactions",
	}
)

func genTxs(ctx *cli.Context) error {
	eth, stack, backend := makeFullNode(ctx)
	startNode(ctx, stack, backend, true)
	defer stack.Close()

	addrs := stack.AccountManager().Accounts()
	apis := eth.APIs()
	var txApi *ethapi.TransactionAPI
	for _, api := range apis {
		if transactionApi, ok := api.Service.(*ethapi.TransactionAPI); ok {
			txApi = transactionApi
			break
		}
	}
	if txApi == nil {
		return errors.New("no getTx API")
	}

	origin, err := eccb.OpenDatabase(ctx.Args().Get(0), true)
	if err != nil {
		return err
	}
	defer eccb.CloseDatabase(origin)
	native, err := eccb.OpenDatabase(ctx.Args().Get(1), false)
	if err != nil {
		return err
	}
	defer eccb.CloseDatabase(native)
	alias, err := eccb.OpenDatabase(ctx.Args().Get(2), false)
	if err != nil {
		return err
	}
	defer eccb.CloseDatabase(alias)

	totalSize, dataSize := uint64(0), uint64(0)
	for i := uint64(0); i < 100; i++ {
		txs, err := eccb.ReadTxs(origin, i)
		if err != nil {
			return err
		}
		for _, tx := range txs {
			totalSize += tx.Size()
			dataSize += uint64(len(tx.Data()))
		}
	}
	rate := 1.0 - float64(totalSize)*0.6/float64(dataSize)

	nonces := make(map[common.Address]uint64)
	for i := uint64(0); i < 100; i++ {
		originTxs, err := eccb.ReadTxs(origin, i)
		if err != nil {
			return err
		}
		nativeTxs := make(types.Transactions, originTxs.Len())
		aliasTxs := make(types.Transactions, originTxs.Len())
		for j, originTx := range originTxs {
			addr := addrs[rand.IntN(len(addrs))]
			account := accounts.Account{Address: addr}
			wallet, err := eth.AccountManager().Find(account)

			value := (*hexutil.Big)(originTx.Value())
			nonce := nonces[addr]
			accessList := originTx.AccessList()
			nonces[addr]++
			args := ethapi.TransactionArgs{
				From:       &addr,
				To:         &addr,
				Value:      value,
				Nonce:      (*hexutil.Uint64)(&nonce),
				AccessList: &accessList,
			}

			input := hexutil.Bytes(originTx.Data())
			args.Data = &input
			if err = args.SetDefaults(context.Background(), backend, false); err != nil {
				return err
			}
			nativeTx, err := wallet.SignTx(account, args.ToTransaction(), backend.ChainConfig().ChainID)
			if err != nil {
				log.Error("Failed to sign transaction", "err", err)
				continue
			}
			nativeTxs[j] = nativeTx

			input = originTx.Data()[:int(float64(len(originTx.Data()))*rate)]
			args.Data = &input
			if err = args.SetDefaults(context.Background(), backend, false); err != nil {
				return err
			}
			aliasTx, err := wallet.SignTx(account, args.ToTransaction(), backend.ChainConfig().ChainID)
			if err != nil {
				log.Error("Failed to sign transaction", "err", err)
				continue
			}
			aliasTxs[j] = aliasTx
		}
		if err = eccb.WriteTxs(native, i+10, nativeTxs); err != nil {
			return err
		}
		if err = eccb.WriteTxs(alias, i+10, aliasTxs); err != nil {
			return err
		}
	}

	return nil
}
