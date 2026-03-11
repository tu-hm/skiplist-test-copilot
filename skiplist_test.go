package skiplist

import (
	"fmt"
	"math/rand"
	"runtime"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

// ── helpers ──────────────────────────────────────────────────────────────────

// keys returns the keys of all live nodes in level-0 order.
func keys(sl *SkipList) []int {
	var out []int
	curr, _ := sl.head.next[0].get()
	for curr != sl.tail {
		_, marked := curr.next[0].get()
		if !marked {
			out = append(out, curr.key)
		}
		curr, _ = curr.next[0].get()
	}
	return out
}

// isSorted returns true if slice is strictly ascending.
func isSorted(s []int) bool {
	for i := 1; i < len(s); i++ {
		if s[i] <= s[i-1] {
			return false
		}
	}
	return true
}

// ── basic sequential tests ────────────────────────────────────────────────────

func TestNew(t *testing.T) {
	sl := New()
	if sl.Len() != 0 {
		t.Fatalf("expected empty list, got Len=%d", sl.Len())
	}
}

func TestAddContains(t *testing.T) {
	sl := New()
	for _, k := range []int{3, 1, 4, 1, 5, 9, 2, 6} {
		sl.Add(k, k*10)
	}
	for _, k := range []int{1, 2, 3, 4, 5, 6, 9} {
		if !sl.Contains(k) {
			t.Errorf("expected Contains(%d)=true", k)
		}
	}
	for _, k := range []int{0, 7, 8, 10} {
		if sl.Contains(k) {
			t.Errorf("expected Contains(%d)=false", k)
		}
	}
}

func TestAddReturnValue(t *testing.T) {
	sl := New()
	if !sl.Add(1, "a") {
		t.Error("first Add should return true (new element)")
	}
	if sl.Add(1, "b") {
		t.Error("second Add with same key should return false (update)")
	}
	// After update, value should be changed.
	n, found := sl.Remove(1)
	if !found || n != "b" {
		t.Errorf("expected value 'b', got %v (found=%v)", n, found)
	}
}

func TestAddOrdered(t *testing.T) {
	sl := New()
	input := []int{5, 3, 7, 1, 9, 2, 8, 4, 6}
	for _, k := range input {
		sl.Add(k, nil)
	}
	ks := keys(sl)
	if !isSorted(ks) {
		t.Errorf("list is not sorted: %v", ks)
	}
	if len(ks) != len(input) {
		t.Errorf("expected %d elements, got %d", len(input), len(ks))
	}
}

func TestRemovePresent(t *testing.T) {
	sl := New()
	sl.Add(10, "ten")
	val, ok := sl.Remove(10)
	if !ok {
		t.Fatal("Remove returned false for present key")
	}
	if val != "ten" {
		t.Errorf("expected value 'ten', got %v", val)
	}
	if sl.Contains(10) {
		t.Error("Contains should be false after Remove")
	}
	if sl.Len() != 0 {
		t.Errorf("expected Len=0, got %d", sl.Len())
	}
}

func TestRemoveAbsent(t *testing.T) {
	sl := New()
	_, ok := sl.Remove(42)
	if ok {
		t.Error("Remove of absent key should return false")
	}
}

func TestRemoveMiddle(t *testing.T) {
	sl := New()
	for _, k := range []int{1, 2, 3, 4, 5} {
		sl.Add(k, nil)
	}
	sl.Remove(3)
	ks := keys(sl)
	for _, k := range ks {
		if k == 3 {
			t.Error("key 3 should have been removed")
		}
	}
	if !isSorted(ks) {
		t.Errorf("list is not sorted after remove: %v", ks)
	}
}

func TestLen(t *testing.T) {
	sl := New()
	for i := 0; i < 100; i++ {
		sl.Add(i, nil)
	}
	if sl.Len() != 100 {
		t.Errorf("expected Len=100, got %d", sl.Len())
	}
	for i := 0; i < 50; i++ {
		sl.Remove(i * 2) // remove even keys
	}
	if sl.Len() != 50 {
		t.Errorf("expected Len=50, got %d", sl.Len())
	}
}

func TestReinsertAfterRemove(t *testing.T) {
	sl := New()
	sl.Add(7, "first")
	sl.Remove(7)
	sl.Add(7, "second")
	if !sl.Contains(7) {
		t.Error("re-inserted key should be present")
	}
	val, ok := sl.Remove(7)
	if !ok || val != "second" {
		t.Errorf("expected 'second', got %v (ok=%v)", val, ok)
	}
}

func TestNegativeKeys(t *testing.T) {
	sl := New()
	for _, k := range []int{-5, -3, -1, 0, 1, 3, 5} {
		sl.Add(k, nil)
	}
	ks := keys(sl)
	if !isSorted(ks) {
		t.Errorf("list with negative keys is not sorted: %v", ks)
	}
	if len(ks) != 7 {
		t.Errorf("expected 7 elements, got %d", len(ks))
	}
}

// ── concurrent tests ──────────────────────────────────────────────────────────

// TestConcurrentAdd spawns many goroutines that each insert a unique key.
func TestConcurrentAdd(t *testing.T) {
	const goroutines = 64
	const perGoroutine = 100

	sl := New()
	var wg sync.WaitGroup
	wg.Add(goroutines)
	for g := 0; g < goroutines; g++ {
		g := g
		go func() {
			defer wg.Done()
			for i := 0; i < perGoroutine; i++ {
				sl.Add(g*perGoroutine+i, nil)
			}
		}()
	}
	wg.Wait()

	expected := goroutines * perGoroutine
	if sl.Len() != expected {
		t.Errorf("expected Len=%d after concurrent inserts, got %d", expected, sl.Len())
	}
	ks := keys(sl)
	if !isSorted(ks) {
		t.Error("list is not sorted after concurrent inserts")
	}
}

// TestConcurrentRemove inserts elements sequentially, then removes them concurrently.
func TestConcurrentRemove(t *testing.T) {
	const n = 1000
	sl := New()
	for i := 0; i < n; i++ {
		sl.Add(i, nil)
	}

	var wg sync.WaitGroup
	var removed atomic.Int64
	wg.Add(n)
	for i := 0; i < n; i++ {
		i := i
		go func() {
			defer wg.Done()
			if _, ok := sl.Remove(i); ok {
				removed.Add(1)
			}
		}()
	}
	wg.Wait()

	if int(removed.Load()) != n {
		t.Errorf("expected %d successful removes, got %d", n, removed.Load())
	}
	if sl.Len() != 0 {
		t.Errorf("expected Len=0 after removing all elements, got %d", sl.Len())
	}
}

// TestConcurrentMixed runs concurrent adds, removes, and contains operations.
func TestConcurrentMixed(t *testing.T) {
	const n = 500
	const goroutines = 8

	sl := New()
	// Pre-populate half the keys.
	for i := 0; i < n/2; i++ {
		sl.Add(i, nil)
	}

	var wg sync.WaitGroup
	wg.Add(goroutines)
	for g := 0; g < goroutines; g++ {
		g := g
		go func() {
			defer wg.Done()
			rng := rand.New(rand.NewSource(time.Now().UnixNano() + int64(g))) //nolint:gosec
			for op := 0; op < 200; op++ {
				key := rng.Intn(n)
				switch rng.Intn(3) {
				case 0:
					sl.Add(key, nil)
				case 1:
					sl.Remove(key)
				case 2:
					sl.Contains(key)
				}
			}
		}()
	}
	wg.Wait()

	// The list must still be sorted and internally consistent.
	ks := keys(sl)
	if !isSorted(ks) {
		t.Error("list is not sorted after mixed concurrent operations")
	}
}

func TestFindConcurrentSafety(t *testing.T) {
	const (
		keySpace = 128
		writers  = 8
		finders  = 8
		iters    = 500
	)

	sl := New()
	for i := 0; i < keySpace; i++ {
		sl.Add(i, nil)
	}

	start := make(chan struct{})
	var wg sync.WaitGroup
	var bad atomic.Bool

	wg.Add(writers + finders)

	for g := 0; g < writers; g++ {
		g := g
		go func() {
			defer wg.Done()
			rng := rand.New(rand.NewSource(int64(10_000 + g)))
			<-start
			for i := 0; i < iters; i++ {
				key := rng.Intn(keySpace)
				if i%2 == 0 {
					sl.Remove(key)
					continue
				}
				sl.Add(key, nil)
			}
		}()
	}

	for g := 0; g < finders; g++ {
		g := g
		go func() {
			defer wg.Done()
			rng := rand.New(rand.NewSource(int64(20_000 + g)))
			preds := make([]*node, maxLevel)
			succs := make([]*node, maxLevel)
			<-start
			for i := 0; i < iters; i++ {
				key := rng.Intn(keySpace)
				sl.find(key, preds, succs)
				for level := 0; level < maxLevel; level++ {
					if preds[level] == nil || succs[level] == nil {
						bad.Store(true)
						return
					}
					if preds[level].key >= key {
						bad.Store(true)
						return
					}
					if succs[level].key < key {
						bad.Store(true)
						return
					}
				}
			}
		}()
	}

	close(start)
	wg.Wait()

	if bad.Load() {
		t.Fatal("find returned invalid predecessor/successor bounds under concurrent access")
	}

	if ks := keys(sl); !isSorted(ks) {
		t.Fatal("list is not sorted after concurrent find activity")
	}
}

// TestNoDeadlock verifies that concurrent operations complete without deadlocking.
// The test binary's -timeout flag (default 10 min) acts as the outer deadline.
func TestNoDeadlock(t *testing.T) {
	sl := New()
	var wg sync.WaitGroup
	for i := 0; i < 10; i++ {
		wg.Add(1)
		i := i
		go func() {
			defer wg.Done()
			for j := 0; j < 100; j++ {
				sl.Add(i*100+j, nil)
				sl.Contains(i * 100)
				sl.Remove(i*100 + j)
			}
		}()
	}
	wg.Wait()
}

func TestLenConcurrentSafety(t *testing.T) {
	const (
		keySpace = 256
		writers  = 8
		readers  = 4
		iters    = 1_000
	)

	sl := New()
	start := make(chan struct{})
	var wg sync.WaitGroup
	var outOfRange atomic.Bool

	wg.Add(writers + readers)

	for g := 0; g < writers; g++ {
		g := g
		go func() {
			defer wg.Done()
			rng := rand.New(rand.NewSource(int64(30_000 + g)))
			<-start
			for i := 0; i < iters; i++ {
				key := rng.Intn(keySpace)
				if rng.Intn(2) == 0 {
					sl.Add(key, nil)
				} else {
					sl.Remove(key)
				}
			}
		}()
	}

	for g := 0; g < readers; g++ {
		go func() {
			defer wg.Done()
			<-start
			for i := 0; i < iters; i++ {
				n := sl.Len()
				if n < 0 || n > keySpace {
					outOfRange.Store(true)
					return
				}
				runtime.Gosched()
			}
		}()
	}

	close(start)
	wg.Wait()

	if outOfRange.Load() {
		t.Fatal("Len returned a value outside the possible key range under concurrent access")
	}

	got := sl.Len()
	want := len(keys(sl))
	if got != want {
		t.Fatalf("Len=%d, want %d after concurrent updates quiesce", got, want)
	}
}

// ── benchmarks ────────────────────────────────────────────────────────────────

func BenchmarkAdd(b *testing.B) {
	sl := New()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		sl.Add(i, nil)
	}
}

func BenchmarkContains(b *testing.B) {
	sl := New()
	for i := 0; i < 10_000; i++ {
		sl.Add(i, nil)
	}
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		sl.Contains(i % 10_000)
	}
}

func BenchmarkRemove(b *testing.B) {
	sl := New()
	for i := 0; i < b.N; i++ {
		sl.Add(i, nil)
	}
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		sl.Remove(i)
	}
}

func BenchmarkConcurrentAdd(b *testing.B) {
	sl := New()
	b.RunParallel(func(pb *testing.PB) {
		i := 0
		for pb.Next() {
			sl.Add(i, nil)
			i++
		}
	})
}

func BenchmarkConcurrentContains(b *testing.B) {
	sl := New()
	for i := 0; i < 10_000; i++ {
		sl.Add(i, nil)
	}
	b.RunParallel(func(pb *testing.PB) {
		i := 0
		for pb.Next() {
			sl.Contains(i % 10_000)
			i++
		}
	})
}

func BenchmarkConcurrentMixed(b *testing.B) {
	sl := New()
	for i := 0; i < 10_000; i++ {
		sl.Add(i, nil)
	}
	var workerID atomic.Int64
	b.RunParallel(func(pb *testing.PB) {
		id := workerID.Add(1)
		rng := rand.New(rand.NewSource(time.Now().UnixNano() + id)) //nolint:gosec
		i := 0
		for pb.Next() {
			key := rng.Intn(10_000)
			switch i % 3 {
			case 0:
				sl.Add(key, nil)
			case 1:
				sl.Remove(key)
			case 2:
				sl.Contains(key)
			}
			i++
		}
	})
}

// ── example ───────────────────────────────────────────────────────────────────

func ExampleSkipList() {
	sl := New()
	sl.Add(3, "three")
	sl.Add(1, "one")
	sl.Add(2, "two")

	fmt.Println(sl.Contains(2))
	fmt.Println(sl.Len())
	sl.Remove(2)
	fmt.Println(sl.Contains(2))
	fmt.Println(sl.Len())
	// Output:
	// true
	// 3
	// false
	// 2
}
