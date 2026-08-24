package types

import (
	"io"
	"slices"
	"sync"

	"github.com/ethereum/go-ethereum/rlp"
)

type Conflicts struct {
	n   int
	out [][]int
	in  [][]int
	rw  sync.RWMutex
}

func NewConflicts(n int) *Conflicts {
	return &Conflicts{n: n, out: make([][]int, n), in: make([][]int, n)}
}

func (cs *Conflicts) Add(u, v int) bool {
	cs.rw.Lock()
	defer cs.rw.Unlock()
	success := false
	index, ok := slices.BinarySearch(cs.out[u], v)
	if !ok {
		cs.out[u] = slices.Insert(cs.out[u], index, v)
		success = true
	}
	index, ok = slices.BinarySearch(cs.in[v], u)
	if !ok {
		cs.in[v] = slices.Insert(cs.in[v], index, u)
		success = true
	}
	return success
}

func (cs *Conflicts) Remove(u, v int) {
	cs.rw.Lock()
	defer cs.rw.Unlock()
	index, ok := slices.BinarySearch(cs.out[u], v)
	if ok {
		cs.out[u] = slices.Delete(cs.out[u], index, index+1)
	}
	index, ok = slices.BinarySearch(cs.in[v], u)
	if ok {
		cs.in[v] = slices.Delete(cs.in[v], index, index+1)
	}
}

func (cs *Conflicts) DirectAncestors(u int) []int {
	cs.rw.RLock()
	defer cs.rw.RUnlock()
	return slices.Clone(cs.in[u])
}

func (cs *Conflicts) DirectAncestorNum(u int) int {
	cs.rw.RLock()
	defer cs.rw.RUnlock()
	return len(cs.in[u])
}

func (cs *Conflicts) DirectDescendants(u int) []int {
	cs.rw.RLock()
	defer cs.rw.RUnlock()
	return slices.Clone(cs.out[u])
}

func (cs *Conflicts) LongestPath() (int, []int) {
	cs.rw.RLock()
	defer cs.rw.RUnlock()
	if cs.n == 0 {
		return 0, nil
	}
	dist := make([]int, cs.n)
	prev := make([]int, cs.n)
	for i := range prev {
		prev[i] = -1
	}
	for u := range cs.n {
		for _, v := range cs.out[u] {
			if dist[u]+1 > dist[v] {
				dist[v] = dist[u] + 1
				prev[v] = u
			}
		}
	}
	best := slices.Max(dist)
	if best == 0 {
		return 0, nil
	}
	t := slices.Index(dist, best)
	path := make([]int, 0, best+1)
	for x := t; x != -1; x = prev[x] {
		path = append(path, x)
	}
	slices.Reverse(path)
	return best, path
}

func (cs *Conflicts) MaxWeightPath(weights []uint64) []int {
	cs.rw.RLock()
	defer cs.rw.RUnlock()
	if len(weights) == 0 {
		return nil
	}

	dp := make([]uint64, len(weights))
	next := make([]int, len(weights))
	for i := range next {
		next[i] = -1
	}

	for i := len(weights) - 1; i >= 0; i-- {
		maxSucc := uint64(0)
		bestChild := -1
		for _, v := range cs.out[i] {
			if dp[v] > maxSucc {
				maxSucc = dp[v]
				bestChild = v
			}
		}
		dp[i] = weights[i] + maxSucc
		next[i] = bestChild
	}

	start := 0
	for i := range dp {
		if dp[i] > dp[start] {
			start = i
		}
	}

	path := make([]int, 0)
	for cur := start; cur != -1; cur = next[cur] {
		path = append(path, cur)
	}

	return path
}

func (cs *Conflicts) TopWeightPaths(weights []uint64, n int) [][]int {
	cs.rw.RLock()
	defer cs.rw.RUnlock()

	topPaths := make([][]int, n)
	if cs.n == 0 || len(weights) != cs.n {
		return topPaths
	}

	csCopy := cs.Copy()
	weightsCopy := slices.Clone(weights)
	for i := range n {
		topPaths[i] = csCopy.MaxWeightPath(weightsCopy)
		for _, node := range topPaths[i] {
			weightsCopy[node] = 0
			for _, to := range csCopy.out[node] {
				csCopy.Remove(node, to)
			}
			for _, from := range csCopy.in[node] {
				csCopy.Remove(from, node)
			}
		}
	}
	return topPaths
}

func (cs *Conflicts) DepNum() int {
	cs.rw.RLock()
	defer cs.rw.RUnlock()
	count := 0
	for _, vs := range cs.out {
		count += len(vs)
	}
	return count
}

func (cs *Conflicts) Flatten() [][2]int {
	cs.rw.RLock()
	defer cs.rw.RUnlock()
	res := make([][2]int, 0)
	for u := range cs.out {
		for _, v := range cs.out[u] {
			res = append(res, [2]int{u, v})
		}
	}
	return res
}

func (cs *Conflicts) Copy() *Conflicts {
	cs.rw.RLock()
	defer cs.rw.RUnlock()
	conflicts := &Conflicts{
		n:   cs.n,
		in:  make([][]int, len(cs.in)),
		out: make([][]int, len(cs.out)),
	}
	for i, list := range cs.in {
		conflicts.in[i] = slices.Clone(list)
	}
	for i, list := range cs.out {
		conflicts.out[i] = slices.Clone(list)
	}
	return conflicts
}

func (cs *Conflicts) EncodeRLP(writer io.Writer) error {
	cs.rw.RLock()
	defer cs.rw.RUnlock()
	enc := make([]uint64, 0)
	enc = append(enc, uint64(cs.n))
	for u, out := range cs.out {
		for _, v := range out {
			enc = append(enc, uint64(u), uint64(v))
		}
	}
	return rlp.Encode(writer, enc)
}

func (cs *Conflicts) DecodeRLP(stream *rlp.Stream) error {
	cs.rw.RLock()
	defer cs.rw.RUnlock()
	var dec []uint64
	if err := stream.Decode(&dec); err != nil {
		return err
	}

	n := int(dec[0])
	cs.n = n
	cs.out = make([][]int, n)
	cs.in = make([][]int, n)
	for i := 0; i < len(dec)/2; i++ {
		u := int(dec[2*i+1])
		v := int(dec[2*i+2])
		cs.out[u] = append(cs.out[u], v)
		cs.in[v] = append(cs.in[v], u)
	}
	for i := range n {
		if cs.in[i] != nil {
			slices.Sort(cs.in[i])
		}
	}
	return nil
}
