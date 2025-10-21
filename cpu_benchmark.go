package main

import (
	"crypto/rand"
	"crypto/sha512"
	"encoding/base64"
	"fmt"
	"os"
	"path/filepath"
	"sync"
	"time"
)

// FileRecord holds the filename and the original hash of its content.
// This information is passed from a producer (createAndHashFile) to a
// consumer (verifyAndCleanWorker) to allow for out-of-band verification.
type FileRecord struct {
	Filename     string
	OriginalHash []byte
}

const (
	NumFiles        = 1000
	FileSizeMB      = 1
	FileSize        = FileSizeMB * 1024 * 1024 // 1MB
	ConsumerWorkers = 10                       // Number of concurrent goroutines reading and verifying files
	TempDir         = "cpu_benchmark_files"
)

// fileChan is a buffered channel used to communicate the FileRecord
// from the producer goroutines to the consumer goroutines.
var fileChan = make(chan FileRecord, NumFiles)

// main is the entry point for the benchmark program. It orchestrates the
// setup, execution, and cleanup of the benchmark.
// The benchmark runs in two main concurrent phases:
// 1. Producers: A pool of goroutines that create, hash, and write files.
// 2. Consumers: A pool of goroutines that read, verify, and delete the files.
// WaitGroups are used to synchronize these phases and ensure all work is complete.
func main() {
	fmt.Printf("Starting CPU and I/O benchmark:\n")
	fmt.Printf("  Files to process: %d\n", NumFiles)
	fmt.Printf("  File size: %d MB\n", FileSizeMB)
	fmt.Printf("  Consumer workers: %d\n\n", ConsumerWorkers)

	// --- Setup ---
	start := time.Now()

	// 1. Create temporary directory for files
	if err := os.MkdirAll(TempDir, 0755); err != nil {
		fmt.Printf("Error creating temp directory: %v\n", err)
		return
	}

	// producerWg waits for all file creation goroutines to finish.
	var producerWg sync.WaitGroup
	// consumerWg waits for all file verification goroutines to finish.
	var consumerWg sync.WaitGroup

	// --- Phase 2: Start consumer worker pool ---
	consumerWg.Add(ConsumerWorkers)
	for i := 0; i < ConsumerWorkers; i++ {
		go verifyAndCleanWorker(i, &consumerWg)
	}

	// --- Phase 1: Start producer goroutines (100 parallel file operations) ---
	producerWg.Add(NumFiles)
	for i := 0; i < NumFiles; i++ {
		go createAndHashFile(i, &producerWg)
	}

	// --- Wait for producers to finish, then close the channel ---
	// This signals to the consumers that no more files will be produced.
	producerWg.Wait()
	close(fileChan)

	// --- Wait for consumers to finish processing all files ---
	consumerWg.Wait()

	duration := time.Since(start)

	// --- Cleanup and Report ---
	if err := os.RemoveAll(TempDir); err != nil {
		fmt.Printf("Error cleaning up temp directory (%s): %v\n", TempDir, err)
	}

	fmt.Printf("\n--- Benchmark Complete ---\n")
	fmt.Printf("Total time taken for all %d files (Create/Hash/Encode/Write/Read/Decode/Verify/Delete): %s\n", NumFiles, duration.Round(time.Millisecond))
	fmt.Printf("Average time per file: %s\n", (duration / NumFiles).Round(time.Microsecond))
}

// createAndHashFile is a "producer" goroutine. It performs the following steps:
//  1. Generates a block of random data.
//  2. Computes the SHA512 hash of the original data.
//  3. Base64-encodes the data.
//  4. Writes the encoded data to a unique file.
//  5. Sends a FileRecord (containing the filename and original hash) to a channel
//     for a consumer goroutine to process.
func createAndHashFile(index int, wg *sync.WaitGroup) {
	defer wg.Done()
	filename := filepath.Join(TempDir, fmt.Sprintf("data_%03d.txt", index))

	// 1. Generate 1MB of random bytes
	randomBytes := make([]byte, FileSize)
	if _, err := rand.Read(randomBytes); err != nil {
		fmt.Printf("Producer %d: Error generating random bytes: %v\n", index, err)
		return
	}

	// 2. Compute SHA512 of the random bytes (original hash)
	hasher := sha512.New()
	hasher.Write(randomBytes)
	originalHash := hasher.Sum(nil)

	// 3. Base64 encode the content
	encodedContent := base64.StdEncoding.EncodeToString(randomBytes)

	// 4. Write the encoded content to the file
	// Note: The encoded content will be about 1.33MB on disk.
	if err := os.WriteFile(filename, []byte(encodedContent), 0644); err != nil {
		fmt.Printf("Producer %d: Error writing file %s: %v\n", index, filename, err)
		return
	}

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
func verifyAndCleanWorker(workerID int, wg *sync.WaitGroup) {
	defer wg.Done()
	// The worker loops until the channel is closed.
	for record := range fileChan {

		// 1. Read the content of the file (base64 encoded)
		encodedBytes, err := os.ReadFile(record.Filename)
		if err != nil {
			fmt.Printf("Worker %d: Error reading file %s: %v\n", workerID, record.Filename, err)
			continue
		}

		// 2. Decode the base64 content
		decodedBytes, err := base64.StdEncoding.DecodeString(string(encodedBytes))
		if err != nil {
			fmt.Printf("Worker %d: Error decoding base64 in file %s: %v\n", workerID, record.Filename, err)
			continue
		}

		// 3. Compute SHA512 of the decoded bytes (new hash)
		hasher := sha512.New()
		hasher.Write(decodedBytes)
		newHash := hasher.Sum(nil)

		// 4. Compare it with the original hash
		// Note: A simple `bytes.Equal(newHash, record.OriginalHash)` is a more
		// idiomatic and safer way to compare byte slices in Go. This manual
		// loop is functionally equivalent for this specific use case.
		match := string(newHash) == string(record.OriginalHash)

		if !match {
			fmt.Printf("Worker %d: INTEGRITY CHECK FAILED (Hash Mismatch) for file %s\n", workerID, record.Filename)
		}

		// 5. Delete the file
		if err := os.Remove(record.Filename); err != nil {
			fmt.Printf("Worker %d: Error deleting file %s: %v\n", workerID, record.Filename, err)
		}
	}
}
