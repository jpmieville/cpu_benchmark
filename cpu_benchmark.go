package main

import (
	"bytes"
	"crypto/rand"
	"crypto/sha512"
	"encoding/base64"
	"flag"
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"runtime/pprof"
	"sync"
	"sync/atomic"
	"time"

	"github.com/shirou/gopsutil/v3/cpu"
)

// FileRecord holds the filename and the original hash of its content.
// This information is passed from a producer (createAndHashFile) to a
// consumer (verifyAndCleanWorker) to allow for out-of-band verification.
type FileRecord struct {
	Filename     string
	OriginalHash []byte
}

// BenchmarkStats tracks various statistics during benchmark execution
type BenchmarkStats struct {
	FilesProcessed   atomic.Int64
	FilesVerified    atomic.Int64
	ErrorsEncountered atomic.Int64
	BytesWritten     atomic.Int64
	BytesRead        atomic.Int64
	StartTime        time.Time
}

// Config holds all configurable parameters for the benchmark
type Config struct {
	NumFiles        int
	FileSizeMB      int
	ConsumerWorkers int
	TempDir         string
	ShowProgress    bool
	CPUProfile      string
	MemProfile      string
}

var (
	// CLI flags
	numFiles        = flag.Int("files", 100, "total number of files to create and verify")
	fileSizeMB      = flag.Int("size", 1, "size of each file in megabytes")
	consumerWorkers = flag.Int("workers", 10, "number of concurrent consumer goroutines")
	tempDir         = flag.String("dir", "cpu_benchmark_files", "temporary directory for benchmark files")
	showProgress    = flag.Bool("progress", true, "show progress updates during benchmark")
	cpuProfile      = flag.String("cpuprofile", "", "write CPU profile to file")
	memProfile      = flag.String("memprofile", "", "write memory profile to file")
)

// main is the entry point for the benchmark program. It orchestrates the
// setup, execution, and cleanup of the benchmark.
// The benchmark runs in two main concurrent phases:
// 1. Producers: A pool of goroutines that create, hash, and write files.
// 2. Consumers: A pool of goroutines that read, verify, and delete the files.
// WaitGroups are used to synchronize these phases and ensure all work is complete.
func main() {
	flag.Parse()

	config := Config{
		NumFiles:        *numFiles,
		FileSizeMB:      *fileSizeMB,
		ConsumerWorkers: *consumerWorkers,
		TempDir:         *tempDir,
		ShowProgress:    *showProgress,
		CPUProfile:      *cpuProfile,
		MemProfile:      *memProfile,
	}

	if err := runBenchmark(config); err != nil {
		fmt.Fprintf(os.Stderr, "Benchmark failed: %v\n", err)
		os.Exit(1)
	}
}

func runBenchmark(config Config) error {
	// Setup CPU profiling
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

	fmt.Printf("Starting CPU and I/O benchmark:\n")
	fmt.Printf("  Files to process: %d\n", config.NumFiles)
	fmt.Printf("  File size: %d MB\n", config.FileSizeMB)
	fmt.Printf("  Consumer workers: %d\n", config.ConsumerWorkers)
	fmt.Printf("  Temp directory: %s\n", config.TempDir)
	if config.CPUProfile != "" {
		fmt.Printf("  CPU profile: %s\n", config.CPUProfile)
	}
	if config.MemProfile != "" {
		fmt.Printf("  Memory profile: %s\n", config.MemProfile)
	}
	fmt.Println()

	// --- Setup ---
	stats := &BenchmarkStats{StartTime: time.Now()}

	// 1. Create temporary directory for files
	if err := os.MkdirAll(config.TempDir, 0755); err != nil {
		return fmt.Errorf("error creating temp directory: %w", err)
	}
	defer func() {
		if err := os.RemoveAll(config.TempDir); err != nil {
			fmt.Printf("Warning: error cleaning up temp directory (%s): %v\n", config.TempDir, err)
		}
	}()

	// Create file channel
	fileChan := make(chan FileRecord, config.NumFiles)

	fileSize := config.FileSizeMB * 1024 * 1024

	// Start progress reporter if enabled
	var progressDone chan struct{}
	if config.ShowProgress {
		progressDone = make(chan struct{})
		go progressReporter(stats, config.NumFiles, progressDone)
	}

	// producerWg waits for all file creation goroutines to finish.
	var producerWg sync.WaitGroup
	// consumerWg waits for all file verification goroutines to finish.
	var consumerWg sync.WaitGroup

	// --- Phase 2: Start consumer worker pool ---
	consumerWg.Add(config.ConsumerWorkers)
	for i := 0; i < config.ConsumerWorkers; i++ {
		go verifyAndCleanWorker(i, fileChan, stats, config.TempDir, &consumerWg)
	}

	// --- Phase 1: Start producer goroutines (parallel file operations) ---
	producerWg.Add(config.NumFiles)
	for i := 0; i < config.NumFiles; i++ {
		go createAndHashFile(i, fileSize, fileChan, stats, config.TempDir, &producerWg)
	}

	// --- Wait for producers to finish, then close the channel ---
	// This signals to the consumers that no more files will be produced.
	producerWg.Wait()
	close(fileChan)

	// --- Wait for consumers to finish processing all files ---
	consumerWg.Wait()

	// Stop progress reporter
	if config.ShowProgress {
		close(progressDone)
		// Wait a moment for the final progress line to be printed
		time.Sleep(50 * time.Millisecond)
		fmt.Println() // Move to new line after progress updates
	}

	duration := time.Since(stats.StartTime)

	// Write memory profile if requested
	if config.MemProfile != "" {
		f, err := os.Create(config.MemProfile)
		if err != nil {
			return fmt.Errorf("could not create memory profile: %w", err)
		}
		defer f.Close()
		runtime.GC() // Get up-to-date statistics
		if err := pprof.WriteHeapProfile(f); err != nil {
			return fmt.Errorf("could not write memory profile: %w", err)
		}
	}

	// --- Report Results ---
	printBenchmarkResults(stats, config, duration)

	if stats.ErrorsEncountered.Load() > 0 {
		return fmt.Errorf("benchmark completed with %d errors", stats.ErrorsEncountered.Load())
	}

	return nil
}

func printBenchmarkResults(stats *BenchmarkStats, config Config, duration time.Duration) {
	fmt.Printf("\n--- Benchmark Complete ---\n")
	fmt.Printf("Total time: %s\n", duration.Round(time.Millisecond))
	fmt.Printf("Files processed: %d\n", stats.FilesProcessed.Load())
	fmt.Printf("Files verified: %d\n", stats.FilesVerified.Load())
	fmt.Printf("Errors encountered: %d\n", stats.ErrorsEncountered.Load())
	fmt.Println()

	// Calculate throughput metrics
	if duration.Seconds() > 0 {
		filesPerSec := float64(stats.FilesProcessed.Load()) / duration.Seconds()
		opsPerSec := filesPerSec * 2 // Each file has create and verify operations
		fmt.Printf("Throughput:\n")
		fmt.Printf("  Files/sec: %.2f\n", filesPerSec)
		fmt.Printf("  Operations/sec: %.2f\n", opsPerSec)

		totalMB := float64(stats.FilesProcessed.Load()) * float64(config.FileSizeMB)
		mbPerSec := totalMB / duration.Seconds()
		fmt.Printf("  MB/sec (raw data): %.2f\n", mbPerSec)

		// Use actual bytes written and read for accurate I/O metrics
		totalBytesIO := float64(stats.BytesWritten.Load() + stats.BytesRead.Load())
		mbIO := totalBytesIO / (1024 * 1024)
		mbPerSecIO := mbIO / duration.Seconds()
		fmt.Printf("  MB/sec (disk I/O): %.2f\n", mbPerSecIO)
		fmt.Println()
	}

	// Calculate average times
	if stats.FilesProcessed.Load() > 0 {
		avgTime := duration / time.Duration(stats.FilesProcessed.Load())
		fmt.Printf("Average time per file: %s\n", avgTime.Round(time.Microsecond))
	}

	// System info
	fmt.Printf("\nSystem info:\n")
	fmt.Printf("  CPU cores: %d\n", runtime.NumCPU())
	fmt.Printf("  GOMAXPROCS: %d\n", runtime.GOMAXPROCS(0))

	// Get CPU model and architecture info
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

func progressReporter(stats *BenchmarkStats, totalFiles int, done chan struct{}) {
	ticker := time.NewTicker(1 * time.Second)
	defer ticker.Stop()

	for {
		select {
		case <-done:
			return
		case <-ticker.C:
			processed := stats.FilesProcessed.Load()
			verified := stats.FilesVerified.Load()
			errors := stats.ErrorsEncountered.Load()
			elapsed := time.Since(stats.StartTime)

			progress := float64(verified) / float64(totalFiles) * 100
			rate := float64(verified) / elapsed.Seconds()

			fmt.Printf("\r[%.1f%%] Created: %d | Verified: %d | Errors: %d | Rate: %.1f files/sec",
				progress, processed, verified, errors, rate)
		}
	}
}

// createAndHashFile is a "producer" goroutine. It performs the following steps:
//  1. Generates a block of random data.
//  2. Computes the SHA512 hash of the original data.
//  3. Base64-encodes the data.
//  4. Writes the encoded data to a unique file.
//  5. Sends a FileRecord (containing the filename and original hash) to a channel
//     for a consumer goroutine to process.
func createAndHashFile(index, fileSize int, fileChan chan<- FileRecord, stats *BenchmarkStats, tempDir string, wg *sync.WaitGroup) {
	defer wg.Done()
	filename := filepath.Join(tempDir, fmt.Sprintf("data_%03d.txt", index))

	// 1. Generate random bytes
	randomBytes := make([]byte, fileSize)
	if _, err := rand.Read(randomBytes); err != nil {
		fmt.Printf("Producer %d: Error generating random bytes: %v\n", index, err)
		stats.ErrorsEncountered.Add(1)
		return
	}

	// 2. Compute SHA512 of the random bytes (original hash)
	hasher := sha512.New()
	hasher.Write(randomBytes)
	originalHash := hasher.Sum(nil)

	// 3. Base64 encode the content
	encodedContent := base64.StdEncoding.EncodeToString(randomBytes)
	encodedBytes := []byte(encodedContent)

	// 4. Write the encoded content to the file
	// Note: The encoded content will be about 1.33x larger on disk.
	if err := os.WriteFile(filename, encodedBytes, 0644); err != nil {
		fmt.Printf("Producer %d: Error writing file %s: %v\n", index, filename, err)
		stats.ErrorsEncountered.Add(1)
		return
	}

	stats.BytesWritten.Add(int64(len(encodedBytes)))
	stats.FilesProcessed.Add(1)

	// 5. Send file record to the verification channel
	fileChan <- FileRecord{
		Filename:     filename,
		OriginalHash: originalHash,
	}
}

// verifyAndCleanWorker is a "consumer" goroutine. It runs in a worker pool
// and performs the following steps in a loop:
// 1. Receives a FileRecord from the file channel.
// 2. Reads the content from the specified file.
// 3. Base64-decodes the content to get the original data.
// 4. Computes the SHA512 hash of the decoded data.
// 5. Compares the new hash with the original hash from the FileRecord to verify integrity.
// 6. Deletes the file.
// The worker exits when the file channel is closed and empty.
func verifyAndCleanWorker(workerID int, fileChan <-chan FileRecord, stats *BenchmarkStats, tempDir string, wg *sync.WaitGroup) {
	defer wg.Done()
	// The worker loops until the channel is closed.
	for record := range fileChan {

		// 1. Read the content of the file (base64 encoded)
		encodedBytes, err := os.ReadFile(record.Filename)
		if err != nil {
			fmt.Printf("Worker %d: Error reading file %s: %v\n", workerID, record.Filename, err)
			stats.ErrorsEncountered.Add(1)
			continue
		}

		stats.BytesRead.Add(int64(len(encodedBytes)))

		// 2. Decode the base64 content
		decodedBytes, err := base64.StdEncoding.DecodeString(string(encodedBytes))
		if err != nil {
			fmt.Printf("Worker %d: Error decoding base64 in file %s: %v\n", workerID, record.Filename, err)
			stats.ErrorsEncountered.Add(1)
			continue
		}

		// 3. Compute SHA512 of the decoded bytes (new hash)
		hasher := sha512.New()
		hasher.Write(decodedBytes)
		newHash := hasher.Sum(nil)

		// 4. Compare it with the original hash using bytes.Equal
		if !bytes.Equal(newHash, record.OriginalHash) {
			fmt.Printf("Worker %d: INTEGRITY CHECK FAILED (Hash Mismatch) for file %s\n", workerID, record.Filename)
			stats.ErrorsEncountered.Add(1)
		} else {
			stats.FilesVerified.Add(1)
		}

		// 5. Delete the file
		if err := os.Remove(record.Filename); err != nil {
			fmt.Printf("Worker %d: Error deleting file %s: %v\n", workerID, record.Filename, err)
			stats.ErrorsEncountered.Add(1)
		}
	}
}
