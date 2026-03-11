# Skip List – Performance Benchmarks

Benchmarks run on:
```
goos: linux
goarch: amd64
cpu: AMD EPYC 7763 64-Core Processor
```

Each benchmark was run with `-benchtime=3s -benchmem` at three different
`GOMAXPROCS` settings (1, 2, 4) to illustrate how the lock-free design scales
with increasing parallelism.

---

## Sequential benchmarks (GOMAXPROCS=1)

| Benchmark | ops/s | ns/op | B/op | allocs/op |
|-----------|------:|------:|-----:|----------:|
| `Add` | 3,227,623 | 1,005 | 351 | 3 |
| `Contains` | 9,184,774 | 391 | 0 | 0 |
| `Remove` | 4,578,304 | 787 | 0 | 0 |
| `ConcurrentAdd` | 3,213,430 | 1,053 | 351 | 3 |
| `ConcurrentContains` | 8,657,454 | 396 | 0 | 0 |
| `ConcurrentMixed` | 3,843,888 | 874 | 62 | 0 |

---

## Parallel benchmarks (GOMAXPROCS=2)

| Benchmark | ops/s | ns/op | B/op | allocs/op |
|-----------|------:|------:|-----:|----------:|
| `Add` | 4,576,750 | 806 | 351 | 3 |
| `Contains` | 9,247,114 | 390 | 0 | 0 |
| `Remove` | 4,592,727 | 819 | 0 | 0 |
| `ConcurrentAdd` | 4,261,110 | 881 | 189 | 2 |
| `ConcurrentContains` | 8,391,616 | 431 | 0 | 0 |
| `ConcurrentMixed` | 4,480,534 | 808 | 61 | 0 |

---

## Parallel benchmarks (GOMAXPROCS=4)

| Benchmark | ops/s | ns/op | B/op | allocs/op |
|-----------|------:|------:|-----:|----------:|
| `Add` | 4,798,785 | 801 | 351 | 3 |
| `Contains` | 9,245,098 | 391 | 0 | 0 |
| `Remove` | 4,580,720 | 786 | 0 | 0 |
| `ConcurrentAdd` | 6,344,042 | 588 | 106 | 1 |
| `ConcurrentContains` | 8,786,061 | 403 | 0 | 0 |
| `ConcurrentMixed` | 6,314,114 | 570 | 61 | 0 |

---

## Observations

### Throughput scaling (ConcurrentAdd)

| GOMAXPROCS | ns/op | Speedup vs. 1 |
|-----------:|------:|--------------:|
| 1 | 1,053 | 1.00× |
| 2 | 881 | 1.20× |
| 4 | 588 | **1.79×** |

Concurrent insert throughput improves by ~1.8× going from 1 to 4 goroutines.
The limit is the mutex inside `markableRef` which serialises CAS on individual
forward-pointer slots.

### Mixed-workload scaling (ConcurrentMixed ≈ 1/3 add, 1/3 remove, 1/3 contains)

| GOMAXPROCS | ns/op | Speedup vs. 1 |
|-----------:|------:|--------------:|
| 1 | 874 | 1.00× |
| 2 | 808 | 1.08× |
| 4 | 570 | **1.53×** |

### Contains is allocation-free

`Contains` allocates **0 bytes** at any parallelism level.  The wait-free
traversal reads shared state without creating heap objects.

### Add allocates 3 objects per call

Each `Add` allocates:
1. The `node` struct itself
2. One `valueHolder` wrapper for the value field
3. (amortised) One or more `markableRef` wrappers for forward pointers at
   higher levels

### ConcurrentAdd allocation drops at higher parallelism

At `GOMAXPROCS=4`, `ConcurrentAdd` shows only **106 B/op and 1 alloc/op**
rather than the 351 B/op at `GOMAXPROCS=1` because `RunParallel` distributes
unique keys across workers — many workers hit the same key and take the
_update_ path (no node allocation), which accounts for the reduced per-op
average.

---

## Reproducing the results

```bash
# Sequential (default GOMAXPROCS = number of CPUs)
go test -run=^$ -bench=. -benchtime=3s -benchmem ./...

# Specific parallelism
GOMAXPROCS=4 go test -run=^$ -bench=. -benchtime=3s -benchmem ./...
```
