package main

import (
	"bytes"
	"compress/gzip"
	"crypto/sha512"
	"encoding/base64"
	"os"
	"path/filepath"
	"sync"
	"testing"
	"time"
)

func TestCreateAndHashFile(t *testing.T) {
	tempDir := t.TempDir()
	fileChan := make(chan FileRecord, 1)
	stats := &BenchmarkStats{StartTime: time.Now()}
	var wg sync.WaitGroup

	fileSize := 1024 // 1KB for faster tests
	wg.Add(1)
	go createAndHashFile(0, fileSize, fileChan, stats, tempDir, 6, &wg)
	wg.Wait()
	close(fileChan)

	// Verify file was created and record sent
	record := <-fileChan
	if record.Filename == "" {
		t.Fatal("Expected filename in FileRecord")
	}

	// Verify file exists
	if _, err := os.Stat(record.Filename); os.IsNotExist(err) {
		t.Fatalf("File was not created: %s", record.Filename)
	}

	// Verify stats were updated
	if stats.FilesProcessed.Load() != 1 {
		t.Errorf("Expected 1 file processed, got %d", stats.FilesProcessed.Load())
	}

	if stats.BytesWritten.Load() == 0 {
		t.Error("Expected bytes written to be > 0")
	}
}

func TestVerifyAndCleanWorker(t *testing.T) {
	tempDir := t.TempDir()
	fileChan := make(chan FileRecord, 1)
	stats := &BenchmarkStats{StartTime: time.Now()}

	// Create a test file with known content
	testData := []byte("test data for verification")
	hasher := sha512.New()
	hasher.Write(testData)
	originalHash := hasher.Sum(nil)

	encodedContent := base64.StdEncoding.EncodeToString(testData)

	// Compress the encoded content with gzip
	var compressedBuf bytes.Buffer
	gzipWriter := gzip.NewWriter(&compressedBuf)
	if _, err := gzipWriter.Write([]byte(encodedContent)); err != nil {
		t.Fatalf("Failed to compress test data: %v", err)
	}
	if err := gzipWriter.Close(); err != nil {
		t.Fatalf("Failed to close gzip writer: %v", err)
	}

	filename := filepath.Join(tempDir, "test_file.txt")
	if err := os.WriteFile(filename, compressedBuf.Bytes(), 0644); err != nil {
		t.Fatalf("Failed to create test file: %v", err)
	}

	// Send record to channel
	fileChan <- FileRecord{
		Filename:     filename,
		OriginalHash: originalHash,
	}
	close(fileChan)

	// Run worker
	var wg sync.WaitGroup
	wg.Add(1)
	go verifyAndCleanWorker(0, fileChan, stats, tempDir, &wg)
	wg.Wait()

	// Verify file was deleted
	if _, err := os.Stat(filename); !os.IsNotExist(err) {
		t.Error("File should have been deleted")
	}

	// Verify stats
	if stats.FilesVerified.Load() != 1 {
		t.Errorf("Expected 1 file verified, got %d", stats.FilesVerified.Load())
	}

	if stats.ErrorsEncountered.Load() != 0 {
		t.Errorf("Expected 0 errors, got %d", stats.ErrorsEncountered.Load())
	}
}

func TestVerifyAndCleanWorkerWithCorruptData(t *testing.T) {
	tempDir := t.TempDir()
	fileChan := make(chan FileRecord, 1)
	stats := &BenchmarkStats{StartTime: time.Now()}

	// Create a test file with corrupted content
	testData := []byte("original data")
	hasher := sha512.New()
	hasher.Write(testData)
	originalHash := hasher.Sum(nil)

	// Write different data to file
	corruptedData := []byte("corrupted data")
	encodedContent := base64.StdEncoding.EncodeToString(corruptedData)

	// Compress the encoded content with gzip
	var compressedBuf bytes.Buffer
	gzipWriter := gzip.NewWriter(&compressedBuf)
	if _, err := gzipWriter.Write([]byte(encodedContent)); err != nil {
		t.Fatalf("Failed to compress test data: %v", err)
	}
	if err := gzipWriter.Close(); err != nil {
		t.Fatalf("Failed to close gzip writer: %v", err)
	}

	filename := filepath.Join(tempDir, "test_file.txt")
	if err := os.WriteFile(filename, compressedBuf.Bytes(), 0644); err != nil {
		t.Fatalf("Failed to create test file: %v", err)
	}

	fileChan <- FileRecord{
		Filename:     filename,
		OriginalHash: originalHash,
	}
	close(fileChan)

	var wg sync.WaitGroup
	wg.Add(1)
	go verifyAndCleanWorker(0, fileChan, stats, tempDir, &wg)
	wg.Wait()

	// Should have detected an error
	if stats.ErrorsEncountered.Load() == 0 {
		t.Error("Expected error to be detected for corrupted data")
	}

	// File should still be verified (attempted)
	if stats.FilesVerified.Load() != 0 {
		t.Errorf("Expected 0 files verified with corruption, got %d", stats.FilesVerified.Load())
	}
}

func TestBytesEqualHashComparison(t *testing.T) {
	// Test that bytes.Equal works correctly for hash comparison
	hash1 := sha512.Sum512([]byte("test data"))
	hash2 := sha512.Sum512([]byte("test data"))
	hash3 := sha512.Sum512([]byte("different data"))

	if !bytes.Equal(hash1[:], hash2[:]) {
		t.Error("Expected identical hashes to be equal")
	}

	if bytes.Equal(hash1[:], hash3[:]) {
		t.Error("Expected different hashes to not be equal")
	}
}

func TestRunBenchmark(t *testing.T) {
	if testing.Short() {
		t.Skip("Skipping benchmark test in short mode")
	}

	tempDir := t.TempDir()
	config := Config{
		NumFiles:         5,
		FileSizeMB:       1,
		ConsumerWorkers:  2,
		TempDir:          tempDir,
		ShowProgress:     false,
		CompressionLevel: 6,
	}

	if err := runBenchmark(config); err != nil {
		t.Fatalf("Benchmark failed: %v", err)
	}

	// Verify temp directory was cleaned up
	if _, err := os.Stat(tempDir); !os.IsNotExist(err) {
		t.Error("Expected temp directory to be cleaned up")
	}
}

func TestConfigValidation(t *testing.T) {
	tests := []struct {
		name    string
		config  Config
		wantErr bool
	}{
		{
			name: "valid config",
			config: Config{
				NumFiles:        10,
				FileSizeMB:      1,
				ConsumerWorkers: 2,
				TempDir:         t.TempDir(),
			},
			wantErr: false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			err := runBenchmark(tt.config)
			if (err != nil) != tt.wantErr {
				t.Errorf("runBenchmark() error = %v, wantErr %v", err, tt.wantErr)
			}
		})
	}
}
