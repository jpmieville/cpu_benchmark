# Go CPU and I/O Benchmark

This is a simple but effective Go program designed to benchmark CPU and I/O performance by performing a series of concurrent file creation, processing, and verification tasks.

It uses a classic producer-consumer pattern to efficiently manage concurrent operations, making it a great example of Go's powerful concurrency features.

## Features

- **CPU-Intensive Work**: Utilizes SHA512 hashing and Base64 encoding/decoding to stress the CPU.
- **I/O-Bound Operations**: Involves writing and reading multi-megabyte files from the disk.
- **High Concurrency**: Leverages Go's concurrency model with goroutines and channels.
- **Producer-Consumer Pattern**: Efficiently separates the work of file creation (producers) from file verification (consumers).
- **Data Integrity Check**: Ensures the correctness of the entire process by comparing cryptographic hashes before and after file operations.
- **Automatic Cleanup**: Creates a temporary directory for its work and removes it upon completion.

## How it Works

The benchmark operates in two main concurrent phases, orchestrated by the `main` function.

1.  **Producer Phase (`createAndHashFile`)**
    - A pool of "producer" goroutines is started (one for each file).
    - Each producer:
        1.  Generates a block of random data in memory.
        2.  Computes the SHA512 hash of this original data.
        3.  Base64-encodes the data.
        4.  Writes the *encoded* data to a unique file in a temporary directory.
        5.  Sends a `FileRecord` (containing the filename and the original hash) to a shared channel.

2.  **Consumer Phase (`verifyAndCleanWorker`)**
    - A worker pool of "consumer" goroutines is started.
    - Each consumer:
        1.  Receives a `FileRecord` from the shared channel.
        2.  Reads the content from the specified file.
        3.  Base64-decodes the content to get the original data back.
        4.  Computes a new SHA512 hash of the decoded data.
        5.  Compares the new hash with the original hash from the `FileRecord` to verify data integrity.
        6.  Deletes the file from the disk.

### Synchronization

- A `sync.WaitGroup` is used to wait for all **producers** to finish their work. Once they are done, the shared channel is closed.
- Closing the channel signals to the **consumers** that no more work will be sent. The consumers will finish processing any remaining items in the channel and then exit.
- A second `sync.WaitGroup` is used to wait for all **consumers** to finish before the program calculates the final duration and exits.

## How to Run

1.  Navigate to the directory containing the source file.
2.  Run the program directly using the Go toolchain:

    ```sh
    go run cpu_benchmark.go
    ```

3.  Alternatively, you can build an executable first and then run it:

    ```sh
    # Build the executable
    go build

    # Run the benchmark (on Linux/macOS)
    ./cpu_benchmark

    # Run the benchmark (on Windows)
    .\cpu_benchmark.exe
    ```

## Configuration

You can easily configure the benchmark by changing the `const` values at the top of the `cpu_benchmark.go` file:

- `NumFiles`: The total number of files to create and process.
- `FileSizeMB`: The size of the initial random data for each file in megabytes.
- `ConsumerWorkers`: The number of concurrent goroutines in the consumer worker pool.
- `TempDir`: The name of the temporary directory where files will be stored.

## Example Output

```
Starting CPU and I/O benchmark:
  Files to process: 1000
  File size: 1 MB
  Consumer workers: 10

--- Benchmark Complete ---
Total time taken for all 1000 files (Create/Hash/Encode/Write/Read/Decode/Verify/Delete): 10.251s
Average time per file: 10.251ms
```

