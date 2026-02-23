# Go CPU and I/O Benchmark

A comprehensive Go program designed to benchmark CPU and I/O performance through concurrent file operations, gzip compression/decompression, cryptographic hashing, and data integrity verification.

This tool provides detailed performance metrics, configurable parameters via CLI flags, optional profiling support, and comprehensive error tracking - making it ideal for both performance testing and learning Go's concurrency patterns.

## Features

### Core Functionality

- **CPU-Intensive Work**: Gzip compression/decompression, SHA512 hashing, and Base64 encoding/decoding to stress the CPU
- **I/O-Bound Operations**: Concurrent writing and reading of configurable file sizes
- **High Concurrency**: Efficient goroutine pooling with configurable worker counts
- **Producer-Consumer Pattern**: Clean separation of file creation and verification
- **Data Integrity Verification**: Cryptographic hash comparison using `bytes.Equal()`
- **Compression Statistics**: Tracks compression ratios and space savings
- **Automatic Cleanup**: Self-managing temporary directory lifecycle

### Performance & Monitoring

- **Detailed Metrics**: Throughput (files/sec, ops/sec, MB/sec), timing statistics
- **Real-time Progress**: Live updates showing creation/verification status and error counts
- **Comprehensive Error Tracking**: Counts and reports all errors encountered during execution
- **System Information**: Reports CPU cores and GOMAXPROCS settings

### Configuration & Profiling

- **CLI Flags**: Fully configurable via command-line arguments
- **Compression Level**: Configurable gzip compression level (1-9)
- **CPU Profiling**: Optional pprof CPU profiling support
- **Memory Profiling**: Optional heap profile generation
- **Flexible Sizing**: Configurable file count, size, and worker pool size

### Testing

- **Unit Tests**: Comprehensive test coverage for all major functions
- **Benchmark Tests**: Go benchmarks for performance measurement and regression testing
  - Individual benchmarks for SHA512 hashing, Base64 encoding/decoding, and gzip compression/decompression
  - Full pipeline benchmark testing the complete workflow

## How It Works

The benchmark operates in two main concurrent phases with a producer-consumer pattern:

### Architecture

1. **Producer Phase (`createAndHashFile`)**
   - Spawns N goroutines (one per file)
   - Each producer:
     1. Generates cryptographically random data
     2. Computes SHA512 hash of original data
     3. Base64-encodes the data (~33% size increase)
     4. Gzip-compresses the encoded data (adds CPU load, reduces disk I/O)
     5. Writes compressed data to unique file
     6. Sends `FileRecord` (filename + hash) to channel
     7. Updates statistics (files processed, bytes written, compression metrics)

2. **Consumer Phase (`verifyAndCleanWorker`)**
   - Worker pool with configurable size
   - Each consumer:
     1. Receives `FileRecord` from channel
     2. Reads compressed file from disk
     3. Gzip-decompresses the data (CPU-intensive)
     4. Base64-decodes to recover original data
     5. Computes SHA512 hash of decoded data
     6. Compares with original hash using `bytes.Equal()`
     7. Deletes file and updates statistics

3. **Progress Reporting** (optional)
   - Separate goroutine updates console every second
   - Shows: progress %, created/verified counts, errors, rate

### Synchronization

- **Producer WaitGroup**: Tracks completion of all file creation goroutines
- **Consumer WaitGroup**: Tracks completion of all worker goroutines
- **Channel Closing**: Signals consumers when no more files will be produced
- **Atomic Operations**: Thread-safe statistics updates via `atomic.Int64`

### Error Handling

- All errors are tracked in atomic counter
- Errors logged to stderr with context (worker ID, filename)
- Benchmark returns error code if any errors encountered
- Continues processing despite individual file failures

### Data Flow

```
[Producer 1] ──┐
[Producer 2] ──┤
[Producer 3] ──┼──> [Channel] ──> [Consumer Worker Pool] ──> [Statistics]
    ...        │                        (10 workers)
[Producer N] ──┘
```

## Installation

```sh
# Clone the repository (if not already done)
git clone https://github.com/jpmieville/cpu_benchmark.git
cd cpu_benchmark

# Build the executable
go build -o cpu_benchmark
```

## Usage

### Basic Usage

Run with default settings (100 files, 1MB each, 10 workers):

```sh
./cpu_benchmark
```

### Command-Line Flags

```sh
./cpu_benchmark [flags]

Flags:
  -files int
        total number of files to create and verify (default 100)
  -size int
        size of each file in megabytes (default 1)
  -workers int
        number of concurrent consumer goroutines (default 10)
  -dir string
        temporary directory for benchmark files (default "cpu_benchmark_files")
  -progress
        show progress updates during benchmark (default true)
  -compress int
        gzip compression level 1-9 (default 6)
  -cpuprofile string
        write CPU profile to file (e.g., "cpu.prof")
  -memprofile string
        write memory profile to file (e.g., "mem.prof")
```

### Examples

**Standard benchmark with 1000 files:**

```sh
./cpu_benchmark -files 1000
```

**Large file benchmark (10MB files):**

```sh
./cpu_benchmark -size 10 -files 100
```

**High concurrency test:**

```sh
./cpu_benchmark -workers 50 -files 500
```

**With compression level (faster, less compression):**

```sh
./cpu_benchmark -compress 1 -files 500
```

**With maximum compression:**

```sh
./cpu_benchmark -compress 9 -files 500
```

**With CPU profiling:**

```sh
./cpu_benchmark -cpuprofile cpu.prof -files 1000
go tool pprof cpu.prof
```

**With memory profiling:**

```sh
./cpu_benchmark -memprofile mem.prof -files 500
go tool pprof mem.prof
```

**Quiet mode (no progress):**

```sh
./cpu_benchmark -progress=false -files 1000
```

**Custom temp directory:**

```sh
./cpu_benchmark -dir /tmp/my_benchmark_files
```

## Testing

### Run Unit Tests

```sh
go test -v
```

### Run Benchmark Tests

```sh
go test -bench=. -benchmem
```

### Run with Race Detector

```sh
go test -race
```

### Test Coverage

```sh
go test -cover
go test -coverprofile=coverage.out
go tool cover -html=coverage.out
```

## Example Output

### Standard Run

```
Starting CPU and I/O benchmark:
  Files to process: 100
  File size: 1 MB
  Consumer workers: 10
  Temp directory: cpu_benchmark_files



--- Benchmark Complete ---
Total time: 340ms
Files processed: 100
Files verified: 100
Errors encountered: 0

Throughput:
  Files/sec: 294.34
  Operations/sec: 588.68
  MB/sec (raw data): 294.34
  MB/sec (disk I/O): 784.91

Compression:
  Before compression: 1333.00 MB
  After compression: 1003.82 MB
  Compression ratio: 75.32%
  Space savings: 24.68%

Average time per file: 3.397ms

System info:
  CPU cores: 8
  GOMAXPROCS: 8
  CPU model: Intel(R) Core(TM) i7-8650U CPU @ 1.90GHz
  CPU architecture: amd64
  CPU frequency: 4.20 GHz

```

### With Profiling

```
Starting CPU and I/O benchmark:
  Files to process: 1000
  File size: 1 MB
  Consumer workers: 10
  Temp directory: cpu_benchmark_files
  CPU profile: cpu.prof
  Memory profile: mem.prof

[... benchmark output ...]
```

## Performance Analysis

### Analyzing CPU Profile

```sh
./cpu_benchmark -cpuprofile cpu.prof -files 1000
go tool pprof cpu.prof

# Interactive commands in pprof:
(pprof) top           # Show top functions by CPU time
(pprof) list main     # Show annotated source for main package
(pprof) web           # Generate SVG graph (requires Graphviz)
```

### Analyzing Memory Profile

```sh
./cpu_benchmark -memprofile mem.prof -files 1000
go tool pprof mem.prof

# Interactive commands:
(pprof) top           # Show top memory allocations
(pprof) list main
(pprof) web
```

## Troubleshooting

### Common Issues

**"Permission denied" errors:**

- Ensure you have write permissions in the directory
- Try specifying a different temp directory: `-dir /tmp/benchmark`

**High memory usage:**

- Reduce file size: `-size 1`
- Reduce concurrent operations: `-workers 5`
- Use memory profiling to identify bottlenecks: `-memprofile mem.prof`

**Slow performance:**

- Check disk I/O (SSD vs HDD makes a significant difference)
- Verify CPU isn't throttled (check `runtime.NumCPU()` output)
- Try adjusting worker count to match CPU cores
- Use CPU profiling: `-cpuprofile cpu.prof`

**Integrity check failures:**

- This should never happen with uncorrupted data
- If you see these, it may indicate:
  - Hardware issues (RAM, disk)
  - Filesystem corruption
  - Race conditions (please report as a bug!)

## Performance Tips

- **For CPU benchmarking**: Use smaller files with more workers
- **For I/O benchmarking**: Use larger files (10MB+)
- **For concurrency testing**: Increase workers to 2-4x CPU core count
- **For production profiling**: Always use `-cpuprofile` and `-memprofile`

## Contributing

Contributions are welcome! Please feel free to submit a Pull Request.

For major changes:

1. Fork the repository
2. Create a feature branch (`git checkout -b feature/amazing-feature`)
3. Make your changes
4. Add tests for new functionality
5. Ensure all tests pass (`go test -v`)
6. Run benchmarks (`go test -bench=.`)
7. Commit your changes (`git commit -m 'Add amazing feature'`)
8. Push to the branch (`git push origin feature/amazing-feature`)
9. Open a Pull Request

### Development

```sh
# Run tests with coverage
go test -v -cover

# Run with race detector
go test -race

# Format code
go fmt ./...

# Run linter (requires golangci-lint)
golangci-lint run
```

## License

MIT License - see [LICENSE](LICENSE) file for details.

## Acknowledgments

- Built with Go's powerful standard library
- Inspired by real-world benchmarking needs
- Thanks to all contributors!
