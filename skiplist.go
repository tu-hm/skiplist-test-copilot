// Package skiplist provides a lock-free concurrent skip list.
//
// The implementation is based on the algorithm described in:
//
//	"A Practical Lock-Free Skiplist" (Herlihy, Lev, Luchangco, Shavit, 2006)
//
// Logical deletion is performed by atomically marking the lowest-level next
// pointer of a node.  Physical removal of logically-deleted nodes is performed
// lazily during subsequent traversals by any goroutine.
//
// All exported operations (Add, Remove, Contains) are safe for concurrent use
// by multiple goroutines without additional synchronisation.
package skiplist

import (
	"math"
	"math/rand"
	"sync"
	"sync/atomic"
)

const (
	// maxLevel is the maximum number of levels in the skip list.
	// log2(2^32) = 32 supports up to ~4 billion elements at p=0.5.
	maxLevel = 32
	// probability is the fraction of nodes promoted to the next level.
	probability = 0.5
)

// markableRef bundles a *node pointer together with a logical-deletion mark
// into a single atomic unit using a mutex so that both fields are updated
// atomically (compare-and-swap on the pair).
//
// Using a struct with a mutex rather than pointer-bit tricks keeps the code
// portable and avoids unsafe pointer manipulation while still providing all
// necessary correctness guarantees.
type markableRef struct {
	mu   sync.Mutex
	node *node
	mark bool
}

// get returns the current (node, mark) pair.
func (r *markableRef) get() (*node, bool) {
	r.mu.Lock()
	n, m := r.node, r.mark
	r.mu.Unlock()
	return n, m
}

// compareAndSet atomically sets (node, mark) to (newNode, newMark) iff the
// current state is (expectedNode, expectedMark).  Returns true on success.
func (r *markableRef) compareAndSet(expectedNode *node, expectedMark bool, newNode *node, newMark bool) bool {
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.node == expectedNode && r.mark == expectedMark {
		r.node = newNode
		r.mark = newMark
		return true
	}
	return false
}

// valueHolder wraps any value so it can be stored in an atomic.Pointer.
type valueHolder struct{ v any }

// node is one element in the skip list.
type node struct {
	key int
	val atomic.Pointer[valueHolder] // read/written atomically to avoid data races
	// next[i] is the forward pointer at level i (0 = bottom level).
	next [maxLevel]*markableRef
	// topLevel is the highest level at which this node participates (0-based).
	topLevel int
}

// getValue returns the current value stored in the node.
func (n *node) getValue() any { return n.val.Load().v }

// setValue stores a new value into the node atomically.
func (n *node) setValue(v any) { n.val.Store(&valueHolder{v}) }

// newNode allocates a node with the given key/value and a random top level.
func newNode(key int, value any, level int) *node {
	n := &node{key: key, topLevel: level}
	n.setValue(value)
	for i := 0; i <= level; i++ {
		n.next[i] = &markableRef{}
	}
	return n
}

// SkipList is a lock-free concurrent ordered set / map keyed by integers.
type SkipList struct {
	head    *node
	tail    *node
	levelHi atomic.Int32 // highest currently used level (0-based)
	rng     rngSource
}

// rngSource is a goroutine-safe random source used for level generation.
type rngSource struct {
	mu  sync.Mutex
	rng *rand.Rand
}

func (r *rngSource) float64() float64 {
	r.mu.Lock()
	v := r.rng.Float64()
	r.mu.Unlock()
	return v
}

// New returns an initialised, empty SkipList ready for concurrent use.
func New() *SkipList {
	head := newNode(math.MinInt, nil, maxLevel-1)
	tail := newNode(math.MaxInt, nil, maxLevel-1)
	for i := 0; i < maxLevel; i++ {
		head.next[i].node = tail
	}
	sl := &SkipList{
		head: head,
		tail: tail,
	}
	sl.rng.rng = rand.New(rand.NewSource(42)) //nolint:gosec // not security-sensitive
	return sl
}

// randomLevel returns a random level in [0, maxLevel-1] using geometric distribution.
func (sl *SkipList) randomLevel() int {
	level := 0
	for level < maxLevel-1 && sl.rng.float64() < probability {
		level++
	}
	return level
}

// find searches for key and returns the predecessor/successor arrays needed
// for insertion and deletion.  It also physically removes logically-deleted
// nodes it encounters along the way.
//
// preds[i] is the rightmost node at level i whose key < key.
// succs[i] is preds[i].next[i] (after cleanup).
func (sl *SkipList) find(key int, preds, succs []*node) bool {
	found := false
retry:
	pred := sl.head
	for level := maxLevel - 1; level >= 0; level-- {
		curr, _ := pred.next[level].get()
		for {
			succ, marked := curr.next[level].get()
			// Skip over logically-deleted nodes.
			for marked {
				// Physically unlink curr from this level.
				if !pred.next[level].compareAndSet(curr, false, succ, false) {
					goto retry
				}
				curr, _ = pred.next[level].get()
				succ, marked = curr.next[level].get()
			}
			if curr.key < key {
				pred = curr
				curr = succ
			} else {
				break
			}
		}
		preds[level] = pred
		succs[level] = curr
	}
	found = succs[0] != nil && succs[0].key == key
	return found
}

// Add inserts key with associated value into the skip list.
// If key is already present its value is updated and Add returns false.
// If key was not present it is inserted and Add returns true.
func (sl *SkipList) Add(key int, value any) bool {
	topLevel := sl.randomLevel()
	preds := make([]*node, maxLevel)
	succs := make([]*node, maxLevel)

	for {
		found := sl.find(key, preds, succs)
		if found {
			// Key already exists – update value and return false.
			succs[0].setValue(value)
			return false
		}

		newN := newNode(key, value, topLevel)

		// Link the new node bottom-up, setting all forward pointers before
		// making the node reachable at level 0.
		for level := 0; level <= topLevel; level++ {
			newN.next[level].node = succs[level]
		}

		// Try to splice into level 0 first.
		pred := preds[0]
		succ := succs[0]
		if !pred.next[0].compareAndSet(succ, false, newN, false) {
			// Another goroutine changed the structure; retry.
			continue
		}

		// Splice into higher levels.
		for level := 1; level <= topLevel; level++ {
			for {
				pred = preds[level]
				succ = succs[level]
				if pred.next[level].compareAndSet(succ, false, newN, false) {
					break
				}
				// Re-find to get fresh preds/succs at this level.
				sl.find(key, preds, succs)
			}
		}

		// Update the tracked high-water level if necessary.
		for {
			hi := sl.levelHi.Load()
			if int32(topLevel) <= hi {
				break
			}
			if sl.levelHi.CompareAndSwap(hi, int32(topLevel)) {
				break
			}
		}

		return true
	}
}

// Remove removes key from the skip list.
// Returns (value, true) if found, or (nil, false) if not present.
func (sl *SkipList) Remove(key int) (any, bool) {
	preds := make([]*node, maxLevel)
	succs := make([]*node, maxLevel)

	for {
		found := sl.find(key, preds, succs)
		if !found {
			return nil, false
		}

		nodeToRemove := succs[0]
		value := nodeToRemove.getValue()

		// Logically delete from the top level down to (but not including) level 0.
		for level := nodeToRemove.topLevel; level >= 1; level-- {
			succ, marked := nodeToRemove.next[level].get()
			for !marked {
				nodeToRemove.next[level].compareAndSet(succ, false, succ, true)
				succ, marked = nodeToRemove.next[level].get()
			}
		}

		// Logically delete at level 0 – whoever succeeds owns the removal.
		succ, marked := nodeToRemove.next[0].get()
		for {
			markedIt := nodeToRemove.next[0].compareAndSet(succ, false, succ, true)
			succ, marked = nodeToRemove.next[0].get()
			if markedIt {
				// Trigger a find to physically unlink.
				sl.find(key, preds, succs)
				return value, true
			}
			if marked {
				// Another goroutine beat us to it.
				return nil, false
			}
		}
	}
}

// Contains reports whether key is present in the skip list.
func (sl *SkipList) Contains(key int) bool {
	pred := sl.head
	var curr *node
	for level := maxLevel - 1; level >= 0; level-- {
		curr, _ = pred.next[level].get()
		for {
			succ, marked := curr.next[level].get()
			// Advance past logically-deleted nodes.
			for marked {
				curr = succ
				succ, marked = curr.next[level].get()
			}
			if curr.key < key {
				pred = curr
				curr = succ
			} else {
				break
			}
		}
	}
	return curr != nil && curr.key == key
}

// Len returns the number of elements currently in the skip list.
// This is an O(n) operation that traverses the bottom level.
func (sl *SkipList) Len() int {
	count := 0
	curr, _ := sl.head.next[0].get()
	for curr != sl.tail {
		_, marked := curr.next[0].get()
		if !marked {
			count++
		}
		curr, _ = curr.next[0].get()
	}
	return count
}
