package types

import (
	"io"
	"slices"

	"github.com/ethereum/go-ethereum/common"
	"github.com/ethereum/go-ethereum/crypto"
	"github.com/ethereum/go-ethereum/rlp"
)

type PreloadSlots struct {
	SlotsList []common.Pair[common.Address, []common.Hash]
	Hashes    map[common.Hash][]common.Hash
}

type preloadSlotsEnc struct {
	Blobs [][]byte
	Paths []uint64
	Slots [][]uint64
}

func TopoSortPaths(paths [][]common.Hash) [][]common.Hash {
	pathSet := make(map[common.Hash]struct{}, len(paths))
	for _, path := range paths {
		pathSet[path[0]] = struct{}{}
	}

	completes := make(map[common.Hash]struct{})
	for _, path := range paths {
		for _, hash := range path[1:] {
			if _, ok := pathSet[hash]; !ok {
				completes[hash] = struct{}{}
			}
		}
	}

	sortedPaths := make([][]common.Hash, 0, len(paths))
	todoList := make([]int, 0)
	for i := range paths {
		todoList = append(todoList, i)
	}

	for len(todoList) > 0 {
		nextTodoList := make([]int, 0)
		for _, index := range todoList {
			flag := true
			for _, hash := range paths[index][1:] {
				if _, ok := completes[hash]; !ok {
					flag = false
					break
				}
			}
			if flag {
				sortedPaths = append(sortedPaths, paths[index])
				completes[paths[index][0]] = struct{}{}
			} else {
				nextTodoList = append(nextTodoList, index)
			}
		}
		todoList = nextTodoList
	}

	return sortedPaths
}

func (ps PreloadSlots) EncodeRLP(writer io.Writer) error {
	tree := make(map[common.Hash][]common.Hash)
	queue := make([]common.Hash, 0)
	for _, slots := range ps.SlotsList {
		tree[common.BytesToHash(slots.First[:])] = nil
		queue = append(queue, slots.Second...)
	}
	for len(queue) != 0 {
		key := queue[len(queue)-1]
		queue = queue[:len(queue)-1]
		if _, ok := tree[key]; ok {
			continue
		}
		if values, ok := ps.Hashes[key]; ok {
			tree[key] = values
			queue = append(queue, values...)
		} else {
			tree[key] = nil
		}
	}

	blobsInHash := make([]common.Hash, 0)
	pathsInHash := make([][]common.Hash, 0)
	for key, values := range tree {
		if values == nil {
			blobsInHash = append(blobsInHash, key)
		} else {
			pathsInHash = append(pathsInHash, append([]common.Hash{key}, values...))
		}
	}
	slices.SortFunc(blobsInHash, common.Hash.Cmp)
	slices.SortFunc(pathsInHash, func(a, b []common.Hash) int { return a[0].Cmp(b[0]) })
	pathsInHash = TopoSortPaths(pathsInHash)

	indexes := make(map[common.Hash]uint64)
	for _, blob := range blobsInHash {
		indexes[blob] = uint64(len(indexes))
	}
	for _, path := range pathsInHash {
		indexes[path[0]] = uint64(len(indexes))
	}

	blobs := make([][]byte, len(blobsInHash))
	for i, blob := range blobsInHash {
		if i == 0 {
			blobs[i] = common.TrimLeftZeroes(blob[:])
		} else {
			blobs[i] = common.TrimLeftZeroes(common.SubHash(blob, blobsInHash[i-1]).Bytes())
		}
	}

	paths := make([]uint64, len(pathsInHash)*2)
	for i, path := range pathsInHash {
		paths[i*2] = indexes[path[1]]
		paths[i*2+1] = indexes[path[2]]
	}

	slots := make([][]uint64, len(ps.SlotsList))
	for i, addrSlots := range ps.SlotsList {
		innerSlots := make([]uint64, 1+len(addrSlots.Second))
		innerSlots[0] = indexes[common.BytesToHash(addrSlots.First[:])]
		for j, slot := range addrSlots.Second {
			innerSlots[j+1] = indexes[slot]
		}
		slots[i] = innerSlots
	}

	enc := preloadSlotsEnc{
		Blobs: blobs,
		Paths: paths,
		Slots: slots,
	}
	return rlp.Encode(writer, enc)
}

func (ps *PreloadSlots) DecodeRLP(stream *rlp.Stream) error {
	var enc preloadSlotsEnc
	if err := stream.Decode(&enc); err != nil {
		return err
	}

	blobsInHash := make([]common.Hash, len(enc.Blobs))
	for i, blob := range enc.Blobs {
		if i == 0 {
			blobsInHash[i] = common.BytesToHash(blob)
		} else {
			blobsInHash[i] = common.AddHash(blobsInHash[i-1], common.BytesToHash(blob))
		}
	}

	pathsInHash := make([][3]common.Hash, len(enc.Paths))
	for i := 0; i < len(enc.Paths)/2; i++ {
		for j := 0; j < 2; j++ {
			if enc.Paths[i*2+j] < uint64(len(enc.Blobs)) {
				pathsInHash[i][1+j] = blobsInHash[enc.Paths[i*2+j]]
			} else {
				pathsInHash[i][1+j] = pathsInHash[enc.Paths[i*2+j]-uint64(len(enc.Blobs))][0]
			}
		}
		pathsInHash[i][0] = crypto.Keccak256Hash(append(pathsInHash[i][1][:], pathsInHash[i][2][:]...))
	}

	slotsList := make([]common.Pair[common.Address, []common.Hash], len(enc.Slots))
	for i, indexes := range enc.Slots {
		addr := common.BytesToAddress(blobsInHash[indexes[0]][12:])

		slots := make([]common.Hash, len(indexes)-1)
		for j, index := range indexes[1:] {
			if index < uint64(len(enc.Blobs)) {
				slots[j] = blobsInHash[index]
			} else {
				slots[j] = pathsInHash[index-uint64(len(enc.Blobs))][0]
			}
		}

		slotsList[i] = common.Pair[common.Address, []common.Hash]{
			First:  addr,
			Second: slots,
		}
	}

	ps.SlotsList = slotsList
	ps.Hashes = nil
	return nil
}
