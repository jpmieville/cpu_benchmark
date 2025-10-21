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

// FileRecord holds the necessary information to verify a file.
type FileRecord struct {
	Filename     string
	OriginalHash []byte
}

const (
	NumFiles        = 100
	FileSizeMB      = 1
	FileSize        = FileSizeMB * 1024 * 1024 // 1MB
	ConsumerWorkers = 10                       // Number of concurrent goroutines reading and verifying files
	TempDir         = "cpu_benchmark_files"
)

// fileChan is a buffered channel used to communicate the FileRecord
// from the producer goroutines to the consumer goroutines.
var fileChan = make(chan FileRecord, NumFiles)
var wg sync.WaitGroup

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

	// The WaitGroup tracks the completion of all file processing tasks (creation + verification).
	wg.Add(NumFiles)

	// --- Phase 2: Start consumer worker pool ---
	for i := 0; i < ConsumerWorkers; i++ {
		go verifyAndCleanWorker(i)
	}

	// --- Phase 1: Start producer goroutines (100 parallel file operations) ---
	for i := 0; i < NumFiles; i++ {
		go createAndHashFile(i)
	}

	// --- Wait for all operations to complete ---
	// Wait will block until the WaitGroup counter is zero.
	wg.Wait()

	// Close the channel once all work is done.
	close(fileChan)

	duration := time.Since(start)

	// --- Cleanup and Report ---
	if err := os.RemoveAll(TempDir); err != nil {
		fmt.Printf("Error cleaning up temp directory (%s): %v\n", TempDir, err)
	}

	fmt.Printf("\n--- Benchmark Complete ---\n")
	fmt.Printf("Total time taken for all %d files (Create/Hash/Encode/Write/Read/Decode/Verify/Delete): %s\n", NumFiles, duration.Round(time.Millisecond))
	fmt.Printf("Average time per file: %s\n", (duration / NumFiles).Round(time.Microsecond))
}

// createAndHashFile generates random bytes, computes SHA512, base64 encodes, and writes to a file.
func createAndHashFile(index int) {
	filename := filepath.Join(TempDir, fmt.Sprintf("data_%03d.txt", index))

	// 1. Generate 1MB of random bytes
	randomBytes := make([]byte, FileSize)
	if _, err := rand.Read(randomBytes); err != nil {
		fmt.Printf("Producer %d: Error generating random bytes: %v\n", index, err)
		// We still need to decrement the counter even if an error occurs
		wg.Done()
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
		// We still need to decrement the counter even if an error occurs
		wg.Done()
		return
	}

	// 5. Send file record to the verification channel
	fileChan <- FileRecord{
		Filename:     filename,
		OriginalHash: originalHash,
	}
}

// verifyAndCleanWorker reads from the channel, verifies the file integrity, and cleans up.
func verifyAndCleanWorker(workerID int) {
	// The worker loops until the channel is closed.
	for record := range fileChan {
		// Signal to the WaitGroup that this specific file processing task is done
		defer wg.Done()

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
		match := true
		if len(newHash) != len(record.OriginalHash) {
			match = false
		} else {
			for i := range newHash {
				if newHash[i] != record.OriginalHash[i] {
					match = false
					break
				}
			}
		}

		if !match {
			fmt.Printf("Worker %d: INTEGRITY CHECK FAILED (Hash Mismatch) for file %s\n", workerID, record.Filename)
		}

		// 5. Delete the file
		if err := os.Remove(record.Filename); err != nil {
			fmt.Printf("Worker %d: Error deleting file %s: %v\n", workerID, record.Filename, err)
		}
	}
}
