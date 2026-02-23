package main

import (
	"bufio"
	"bytes"
	"compress/gzip"
	"crypto/rand"
	"crypto/sha512"
	"encoding/base64"
	"flag"
	"fmt"
	"io"
	"os"
	"os/signal"
	"path/filepath"
	"runtime"
	"runtime/pprof"
	"sync"
	"sync/atomic"
	"time"

	"github.com/shirou/gopsutil/v3/cpu"
)

// FileRecord holds metadata about a processed file.
// The Filename is the path to the file, and OriginalHash is the SHA512 hash
// computed before writing. This allows downstream verification.
type FileRecord struct {
	Filename     string
	OriginalHash []byte
}

// BenchmarkStats tracks various metrics during benchmark execution.
// Atomic integers are used for thread-safe concurrent updates from multiple goroutines.
type BenchmarkStats struct {
	FilesProcessed      atomic.Int64 // Total files created by producers
	FilesVerified       atomic.Int64 // Files successfully verified by consumers
	ErrorsEncountered   atomic.Int64 // Total errors encountered
	BytesWritten        atomic.Int64 // Total bytes written to disk
	BytesRead           atomic.Int64 // Total bytes read from disk
	BytesBeforeCompress atomic.Int64 // Size before gzip compression
	BytesAfterCompress  atomic.Int64 // Size after gzip compression
	StartTime           time.Time    // Benchmark start time for duration calculation
}

// Config holds all configurable parameters for the benchmark.
// These values are typically set via CLI flags in main().
type Config struct {
	NumFiles         int    // Total number of files to create and process
	FileSizeMB       int    // Size of each file in megabytes
	ConsumerWorkers  int    // Number of concurrent consumer goroutines
	TempDir          string // Directory for temporary benchmark files
	ShowProgress     bool   // Whether to display progress updates
	CPUProfile       string // Optional path for CPU profile output
	MemProfile       string // Optional path for memory profile output
	CompressionLevel int    // Gzip compression level (1-9)
}

// CLI flags - these are parsed in main() before creating the Config
var (
	numFiles         = flag.Int("files", 100, "total number of files to create and verify")
	fileSizeMB       = flag.Int("size", 1, "size of each file in megabytes")
	consumerWorkers  = flag.Int("workers", 10, "number of concurrent consumer goroutines")
	tempDir          = flag.String("dir", "cpu_benchmark_files", "temporary directory for benchmark files")
	showProgress     = flag.Bool("progress", true, "show progress updates during benchmark")
	cpuProfile       = flag.String("cpuprofile", "", "write CPU profile to file")
	memProfile       = flag.String("memprofile", "", "write memory profile to file")
	compressionLevel = flag.Int("compress", 6, "gzip compression level (1-9)")
)

// bufferPool is a sync.Pool for reusing byte buffers across goroutines.
// This reduces memory allocations when compressing data.
var bufferPool sync.Pool

// quitChan is used to signal graceful shutdown when the program receives Ctrl+C.
// All goroutines check this channel and exit cleanly when it's closed.
var quitChan chan struct{}

// init initializes the buffer pool with a factory function.
// sync.Pool automatically manages memory reuse across concurrent operations.
func init() {
	bufferPool = sync.Pool{
		New: func() interface{} {
			return new(bytes.Buffer)
		},
	}
}

// Validate checks that all configuration values are within acceptable ranges.
// Returns an error if any validation fails, which causes the program to exit.
func (c *Config) Validate() error {
	if c.NumFiles <= 0 {
		return fmt.Errorf("numFiles must be > 0, got %d", c.NumFiles)
	}
	if c.FileSizeMB <= 0 {
		return fmt.Errorf("fileSizeMB must be > 0, got %d", c.FileSizeMB)
	}
	if c.ConsumerWorkers <= 0 {
		return fmt.Errorf("consumerWorkers must be > 0, got %d", c.ConsumerWorkers)
	}
	if c.CompressionLevel < 1 || c.CompressionLevel > 9 {
		return fmt.Errorf("compressionLevel must be 1-9, got %d", c.CompressionLevel)
	}
	return nil
}

// main is the entry point for the benchmark program.
// It parses flags, validates config, sets up signal handling, and runs the benchmark.
func main() {
	flag.Parse()

	config := Config{
		NumFiles:         *numFiles,
		FileSizeMB:       *fileSizeMB,
		ConsumerWorkers:  *consumerWorkers,
		TempDir:          *tempDir,
		ShowProgress:     *showProgress,
		CPUProfile:       *cpuProfile,
		MemProfile:       *memProfile,
		CompressionLevel: *compressionLevel,
	}

	// Validate configuration before starting
	if err := config.Validate(); err != nil {
		fmt.Fprintf(os.Stderr, "Invalid config: %v\n", err)
		os.Exit(1)
	}

	// Initialize quit channel for graceful shutdown
	quitChan = make(chan struct{})
	defer close(quitChan)

	// Set up signal handling for Ctrl+C (SIGINT)
	// This allows the program to clean up temporary files before exiting
	signalChan := make(chan os.Signal, 1)
	signal.Notify(signalChan, os.Interrupt)
	defer signal.Stop(signalChan)

	// Goroutine to handle interrupt signal
	go func() {
		<-signalChan
		fmt.Println("\nReceived interrupt signal. Cleaning up...")
		close(quitChan)
	}()

	// Run the benchmark
	if err := runBenchmark(config); err != nil {
		fmt.Fprintf(os.Stderr, "Benchmark failed: %v\n", err)
		os.Exit(1)
	}
}

// runBenchmark orchestrates the entire benchmark execution.
// It follows a producer-consumer pattern:
//   - Producers (createAndHashFile): Generate, compress, hash, and write files
//   - Consumers (verifyAndCleanWorker): Read, decompress, verify, and delete files
func runBenchmark(config Config) error {
	// Start CPU profiling if requested
	if config.CPUProfile != "" {
		f, err := os.Create(config.CPUProfile)
		if err != nil {
			return fmt.Errorf("could not create CPU profile: %w", err)
		}
		defer f.Close()
		if err := pprof.StartCPUProfile(f); err != nil {
			return fmt.Errorf("could not start CPU profile: %w", err)
		}
		defer pprof.StopCPUProfile()
	}

	// Print benchmark configuration
	fmt.Printf("Starting CPU and I/O benchmark:\n")
	fmt.Printf("  Files to process: %d\n", config.NumFiles)
	fmt.Printf("  File size: %d MB\n", config.FileSizeMB)
	fmt.Printf("  Consumer workers: %d\n", config.ConsumerWorkers)
	fmt.Printf("  Temp directory: %s\n", config.TempDir)
	fmt.Printf("  Compression level: %d\n", config.CompressionLevel)
	if config.CPUProfile != "" {
		fmt.Printf("  CPU profile: %s\n", config.CPUProfile)
	}
	if config.MemProfile != "" {
		fmt.Printf("  Memory profile: %s\n", config.MemProfile)
	}
	fmt.Println()

	// Initialize statistics tracker
	stats := &BenchmarkStats{StartTime: time.Now()}

	// Create temporary directory for benchmark files
	if err := os.MkdirAll(config.TempDir, 0755); err != nil {
		return fmt.Errorf("error creating temp directory: %w", err)
	}
	// Clean up temp directory on exit (defer runs even on error)
	defer func() {
		if err := os.RemoveAll(config.TempDir); err != nil {
			fmt.Printf("Warning: error cleaning up temp directory (%s): %v\n", config.TempDir, err)
		}
	}()

	// Channel for passing file records from producers to consumers
	// Buffered to allow producers to run ahead without blocking
	fileChan := make(chan FileRecord, config.NumFiles)

	// Calculate actual byte size from MB
	fileSize := config.FileSizeMB * 1024 * 1024

	// Start progress reporter goroutine if enabled
	var progressDone chan struct{}
	if config.ShowProgress {
		progressDone = make(chan struct{})
		go progressReporter(stats, config.NumFiles, progressDone)
	}

	// WaitGroups to track goroutine completion
	var producerWg sync.WaitGroup // Tracks file creation completion
	var consumerWg sync.WaitGroup // Tracks verification completion

	// Start consumer worker pool - these verify and clean files
	// Workers run continuously, processing from the channel until it's closed
	consumerWg.Add(config.ConsumerWorkers)
	for i := 0; i < config.ConsumerWorkers; i++ {
		go verifyAndCleanWorker(i, fileChan, stats, config.TempDir, &consumerWg)
	}

	// Start producer goroutines - each creates one file
	// These generate random data, compress it, hash it, and write to disk
	producerWg.Add(config.NumFiles)
	for i := 0; i < config.NumFiles; i++ {
		go createAndHashFile(i, fileSize, fileChan, stats, config.TempDir, config.CompressionLevel, &producerWg)
	}

	// Wait for all producers to finish, then close the channel
	// This signals to consumers that no more files will arrive
	producerWg.Wait()
	close(fileChan)

	// Wait for all consumers to finish processing
	consumerWg.Wait()

	// Stop progress reporter
	if config.ShowProgress {
		close(progressDone)
		time.Sleep(50 * time.Millisecond) // Allow final progress line to print
		fmt.Println()
	}

	// Calculate total duration
	duration := time.Since(stats.StartTime)

	// Write memory profile if requested
	if config.MemProfile != "" {
		f, err := os.Create(config.MemProfile)
		if err != nil {
			return fmt.Errorf("could not create memory profile: %w", err)
		}
		defer f.Close()
		runtime.GC() // Force garbage collection for accurate heap stats
		if err := pprof.WriteHeapProfile(f); err != nil {
			return fmt.Errorf("could not write memory profile: %w", err)
		}
	}

	// Print final results
	printBenchmarkResults(stats, config, duration)

	// Return error if any errors occurred during benchmark
	if stats.ErrorsEncountered.Load() > 0 {
		return fmt.Errorf("benchmark completed with %d errors", stats.ErrorsEncountered.Load())
	}

	return nil
}

// printBenchmarkResults prints a comprehensive summary of benchmark results.
// Includes throughput metrics, compression statistics, and system information.
func printBenchmarkResults(stats *BenchmarkStats, config Config, duration time.Duration) {
	fmt.Printf("\n--- Benchmark Complete ---\n")
	fmt.Printf("Total time: %s\n", duration.Round(time.Millisecond))
	fmt.Printf("Files processed: %d\n", stats.FilesProcessed.Load())
	fmt.Printf("Files verified: %d\n", stats.FilesVerified.Load())
	fmt.Printf("Errors encountered: %d\n", stats.ErrorsEncountered.Load())
	fmt.Println()

	// Calculate and display throughput metrics
	if duration.Seconds() > 0 {
		filesPerSec := float64(stats.FilesProcessed.Load()) / duration.Seconds()
		opsPerSec := filesPerSec * 2 // Each file has create + verify operations
		fmt.Printf("Throughput:\n")
		fmt.Printf("  Files/sec: %.2f\n", filesPerSec)
		fmt.Printf("  Operations/sec: %.2f\n", opsPerSec)

		// Raw data throughput (size of original files)
		totalMB := float64(stats.FilesProcessed.Load()) * float64(config.FileSizeMB)
		mbPerSec := totalMB / duration.Seconds()
		fmt.Printf("  MB/sec (raw data): %.2f\n", mbPerSec)

		// Actual disk I/O throughput (includes compression overhead)
		totalBytesIO := float64(stats.BytesWritten.Load() + stats.BytesRead.Load())
		mbIO := totalBytesIO / (1024 * 1024)
		mbPerSecIO := mbIO / duration.Seconds()
		fmt.Printf("  MB/sec (disk I/O): %.2f\n", mbPerSecIO)
		fmt.Println()
	}

	// Display compression statistics
	if stats.BytesBeforeCompress.Load() > 0 {
		compressionRatio := float64(stats.BytesAfterCompress.Load()) / float64(stats.BytesBeforeCompress.Load())
		spaceSavings := (1.0 - compressionRatio) * 100
		fmt.Printf("Compression:\n")
		fmt.Printf("  Before compression: %.2f MB\n", float64(stats.BytesBeforeCompress.Load())/(1024*1024))
		fmt.Printf("  After compression: %.2f MB\n", float64(stats.BytesAfterCompress.Load())/(1024*1024))
		fmt.Printf("  Compression ratio: %.2f%%\n", compressionRatio*100)
		fmt.Printf("  Space savings: %.2f%%\n", spaceSavings)
		fmt.Println()
	}

	// Average time per file
	if stats.FilesProcessed.Load() > 0 {
		avgTime := duration / time.Duration(stats.FilesProcessed.Load())
		fmt.Printf("Average time per file: %s\n", avgTime.Round(time.Microsecond))
	}

	// Display system information
	fmt.Printf("\nSystem info:\n")
	fmt.Printf("  CPU cores: %d\n", runtime.NumCPU())
	fmt.Printf("  GOMAXPROCS: %d\n", runtime.GOMAXPROCS(0))

	// Attempt to get CPU model information using gopsutil
	if cpuInfo, err := cpu.Info(); err == nil && len(cpuInfo) > 0 {
		fmt.Printf("  CPU model: %s\n", cpuInfo[0].ModelName)
		fmt.Printf("  CPU architecture: %s\n", runtime.GOARCH)
		if cpuInfo[0].Mhz > 0 {
			fmt.Printf("  CPU frequency: %.2f GHz\n", cpuInfo[0].Mhz/1000.0)
		}
	} else {
		fmt.Printf("  CPU architecture: %s\n", runtime.GOARCH)
	}
}

// progressReporter runs as a goroutine, displaying progress updates every second.
// It monitors the BenchmarkStats and calculates real-time throughput.
// The goroutine exits when either done or quitChan is closed.
func progressReporter(stats *BenchmarkStats, totalFiles int, done chan struct{}) {
	ticker := time.NewTicker(1 * time.Second)
	defer ticker.Stop()

	for {
		select {
		case <-done:
			return // Progress reporting disabled
		case <-quitChan:
			return // Graceful shutdown requested
		case <-ticker.C:
			// Load current statistics (atomic operations)
			processed := stats.FilesProcessed.Load()
			verified := stats.FilesVerified.Load()
			errors := stats.ErrorsEncountered.Load()
			elapsed := time.Since(stats.StartTime)

			// Calculate progress percentage and rate
			progress := float64(verified) / float64(totalFiles) * 100
			rate := float64(verified) / elapsed.Seconds()

			// Print progress to same line (carriage return resets cursor)
			fmt.Printf("\r[%.1f%%] Created: %d | Verified: %d | Errors: %d | Rate: %.1f files/sec",
				progress, processed, verified, errors, rate)
		}
	}
}

// createAndHashFile is a producer goroutine that performs file creation.
// It generates random data, computes a hash, compresses the data, and writes to disk.
// The function implements the following pipeline:
//  1. Generate cryptographically random bytes
//  2. Compute SHA512 hash of the original data
//  3. Base64 encode the data (increases size by ~33%)
//  4. Gzip compress the encoded data
//  5. Write compressed data to file using buffered I/O
//  6. Send FileRecord to channel for consumer verification
func createAndHashFile(index, fileSize int, fileChan chan<- FileRecord, stats *BenchmarkStats, tempDir string, compressLevel int, wg *sync.WaitGroup) {
	defer wg.Done()

	// Check for graceful shutdown request before starting
	select {
	case <-quitChan:
		return
	default:
	}

	// Generate unique filename for this file
	filename := filepath.Join(tempDir, fmt.Sprintf("data_%03d.txt", index))

	// Get buffer from pool to reduce allocations
	buf := bufferPool.Get().(*bytes.Buffer)
	buf.Reset()
	defer bufferPool.Put(buf)

	// Step 1: Generate random bytes using crypto/rand (cryptographically secure)
	randomBytes := make([]byte, fileSize)
	if _, err := rand.Read(randomBytes); err != nil {
		fmt.Printf("Producer %d: Error generating random bytes: %v\n", index, err)
		stats.ErrorsEncountered.Add(1)
		return
	}

	// Step 2: Compute SHA512 hash of the original data
	// This hash is used later to verify data integrity
	hasher := sha512.New()
	hasher.Write(randomBytes)
	originalHash := hasher.Sum(nil)

	// Step 3: Base64 encode the random bytes
	// Base64 increases size by approximately 33%
	encodedContent := base64.StdEncoding.EncodeToString(randomBytes)
	encodedBytes := []byte(encodedContent)

	// Step 4: Gzip compress the encoded data
	// This adds CPU load while reducing I/O
	buf.Reset()
	gzipWriter, err := gzip.NewWriterLevel(buf, compressLevel)
	if err != nil {
		fmt.Printf("Producer %d: Error creating gzip writer: %v\n", index, err)
		stats.ErrorsEncountered.Add(1)
		return
	}
	if _, err := gzipWriter.Write(encodedBytes); err != nil {
		fmt.Printf("Producer %d: Error compressing data: %v\n", index, err)
		stats.ErrorsEncountered.Add(1)
		return
	}
	if err := gzipWriter.Close(); err != nil {
		fmt.Printf("Producer %d: Error closing gzip writer: %v\n", index, err)
		stats.ErrorsEncountered.Add(1)
		return
	}
	compressedBytes := buf.Bytes()

	// Step 5: Write compressed data to file using buffered I/O
	// Buffered writer improves disk I/O performance
	file, err := os.Create(filename)
	if err != nil {
		fmt.Printf("Producer %d: Error creating file %s: %v\n", index, filename, err)
		stats.ErrorsEncountered.Add(1)
		return
	}
	writer := bufio.NewWriter(file)
	if _, err := writer.Write(compressedBytes); err != nil {
		fmt.Printf("Producer %d: Error writing file %s: %v\n", index, filename, err)
		stats.ErrorsEncountered.Add(1)
		file.Close()
		return
	}
	if err := writer.Flush(); err != nil {
		fmt.Printf("Producer %d: Error flushing file %s: %v\n", index, filename, err)
		stats.ErrorsEncountered.Add(1)
		file.Close()
		return
	}
	file.Close()

	// Update statistics
	stats.BytesWritten.Add(int64(len(compressedBytes)))
	stats.BytesBeforeCompress.Add(int64(len(encodedBytes)))
	stats.BytesAfterCompress.Add(int64(len(compressedBytes)))
	stats.FilesProcessed.Add(1)

	// Step 6: Send file record to consumer channel
	// This contains the filename and original hash for verification
	fileChan <- FileRecord{
		Filename:     filename,
		OriginalHash: originalHash,
	}
}

// verifyAndCleanWorker is a consumer goroutine that verifies file integrity.
// It runs in a worker pool, processing files from the channel until closed.
// The function performs the inverse operations of createAndHashFile:
//  1. Read compressed file from disk
//  2. Gzip decompress the data
//  3. Base64 decode to recover original data
//  4. Compute SHA512 hash of decoded data
//  5. Compare with original hash (integrity check)
//  6. Delete the file
func verifyAndCleanWorker(workerID int, fileChan <-chan FileRecord, stats *BenchmarkStats, tempDir string, wg *sync.WaitGroup) {
	defer wg.Done()

	// Get buffer from pool for reuse
	buf := bufferPool.Get().(*bytes.Buffer)
	buf.Reset()
	defer bufferPool.Put(buf)

	// Process files from channel until it's closed
	for record := range fileChan {
		// Check for shutdown signal between files
		select {
		case <-quitChan:
			return
		default:
		}

		// Step 1: Read the compressed file from disk
		compressedBytes, err := os.ReadFile(record.Filename)
		if err != nil {
			fmt.Printf("Worker %d: Error reading file %s: %v\n", workerID, record.Filename, err)
			stats.ErrorsEncountered.Add(1)
			continue
		}

		stats.BytesRead.Add(int64(len(compressedBytes)))

		// Step 2: Gzip decompress the data
		gzipReader, err := gzip.NewReader(bytes.NewReader(compressedBytes))
		if err != nil {
			fmt.Printf("Worker %d: Error creating gzip reader for file %s: %v\n", workerID, record.Filename, err)
			stats.ErrorsEncountered.Add(1)
			continue
		}
		encodedBytes, err := io.ReadAll(gzipReader)
		gzipReader.Close()
		if err != nil {
			fmt.Printf("Worker %d: Error decompressing file %s: %v\n", workerID, record.Filename, err)
			stats.ErrorsEncountered.Add(1)
			continue
		}

		// Step 3: Base64 decode to recover original data
		decodedBytes, err := base64.StdEncoding.DecodeString(string(encodedBytes))
		if err != nil {
			fmt.Printf("Worker %d: Error decoding base64 in file %s: %v\n", workerID, record.Filename, err)
			stats.ErrorsEncountered.Add(1)
			continue
		}

		// Step 4: Compute SHA512 hash of the decoded data
		hasher := sha512.New()
		hasher.Write(decodedBytes)
		newHash := hasher.Sum(nil)

		// Step 5: Compare hashes to verify data integrity
		if !bytes.Equal(newHash, record.OriginalHash) {
			fmt.Printf("Worker %d: INTEGRITY CHECK FAILED (Hash Mismatch) for file %s\n", workerID, record.Filename)
			stats.ErrorsEncountered.Add(1)
		} else {
			stats.FilesVerified.Add(1)
		}

		// Step 6: Delete the file (cleanup)
		// Files are deleted after verification to avoid data loss
		if err := os.Remove(record.Filename); err != nil {
			fmt.Printf("Worker %d: Error deleting file %s: %v\n", workerID, record.Filename, err)
			stats.ErrorsEncountered.Add(1)
		}
	}
}
