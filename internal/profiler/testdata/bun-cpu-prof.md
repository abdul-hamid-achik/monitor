# CPU Profile

| Duration | Samples | Interval | Functions |
|----------|---------|----------|----------|
| 1.49s | 1193 | 1.0ms | 6 |

**Top 10:** `stringify` 51.6%, `repeat` 36.0%, `heavyStringify` 8.9%, `heavyStringify` 2.9%, `heavyStringify` 0.4%

## Hot Functions (Self Time)

| Self% | Self | Total% | Total | Function | Location |
|------:|-----:|-------:|------:|----------|----------|
| 51.6% | 773.5ms | 51.6% | 773.5ms | `stringify` | `[native code]` |
| 36.0% | 540.7ms | 36.0% | 540.7ms | `repeat` | `[native code]` |
| 8.9% | 133.7ms | 60.5% | 907.3ms | `heavyStringify` | `/repo/internal/profiler/testdata/src/hot.js:5` |
| 2.9% | 43.8ms | 2.9% | 43.8ms | `heavyStringify` | `/repo/internal/profiler/testdata/src/hot.js:3` |
| 0.4% | 7.2ms | 36.5% | 547.9ms | `heavyStringify` | `/repo/internal/profiler/testdata/src/hot.js:4` |

## Call Tree (Total Time)

| Total% | Total | Self% | Self | Function | Location |
|-------:|------:|------:|-----:|----------|----------|
| 100.0% | 1.49s | 0.0% | 0us | `(module)` | `/repo/internal/profiler/testdata/src/hot.js:12` |
| 60.5% | 907.3ms | 8.9% | 133.7ms | `heavyStringify` | `/repo/internal/profiler/testdata/src/hot.js:5` |
| 51.6% | 773.5ms | 51.6% | 773.5ms | `stringify` | `[native code]` |
| 36.5% | 547.9ms | 0.4% | 7.2ms | `heavyStringify` | `/repo/internal/profiler/testdata/src/hot.js:4` |
| 36.0% | 540.7ms | 36.0% | 540.7ms | `repeat` | `[native code]` |
| 2.9% | 43.8ms | 2.9% | 43.8ms | `heavyStringify` | `/repo/internal/profiler/testdata/src/hot.js:3` |

## Function Details

### `stringify`
`[native code]` | Self: 51.6% (773.5ms) | Total: 51.6% (773.5ms) | Samples: 618

**Called by:**
- `heavyStringify` (618)

### `repeat`
`[native code]` | Self: 36.0% (540.7ms) | Total: 36.0% (540.7ms) | Samples: 429

**Called by:**
- `heavyStringify` (429)

### `heavyStringify`
`/repo/internal/profiler/testdata/src/hot.js:5` | Self: 8.9% (133.7ms) | Total: 60.5% (907.3ms) | Samples: 106

**Called by:**
- `(module)` (724)

**Calls:**
- `stringify` (618)

### `heavyStringify`
`/repo/internal/profiler/testdata/src/hot.js:3` | Self: 2.9% (43.8ms) | Total: 2.9% (43.8ms) | Samples: 34

**Called by:**
- `(module)` (34)

### `heavyStringify`
`/repo/internal/profiler/testdata/src/hot.js:4` | Self: 0.4% (7.2ms) | Total: 36.5% (547.9ms) | Samples: 6

**Called by:**
- `(module)` (435)

**Calls:**
- `repeat` (429)

### `(module)`
`/repo/internal/profiler/testdata/src/hot.js:12` | Self: 0.0% (0us) | Total: 100.0% (1.49s) | Samples: 0

**Calls:**
- `heavyStringify` (724)
- `heavyStringify` (435)
- `heavyStringify` (34)

## Files

| Self% | Self | File |
|------:|-----:|------|
| 87.6% | 1.31s | `[native code]` |
| 12.3% | 184.8ms | `/repo/internal/profiler/testdata/src/hot.js` |
