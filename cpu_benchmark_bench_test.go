package main

import (
	"bytes"
	"compress/gzip"
	"crypto/rand"
	"crypto/sha512"
	"encoding/base64"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"sync"
	"testing"
	"time"
)

func BenchmarkCreateAndHashFile(b *testing.B) {
	tempDir := b.TempDir()
	fileChan := make(chan FileRecord, b.N)
	stats := &BenchmarkStats{StartTime: time.Now()}
	var wg sync.WaitGroup
	fileSize := 1024 * 1024 // 1MB

	b.ResetTimer()
	b.ReportAllocs()

	for i := 0; i < b.N; i++ {
		wg.Add(1)
		go createAndHashFile(i, fileSize, fileChan, stats, tempDir, 6, &wg)
	}

	wg.Wait()
	close(fileChan)
}

func BenchmarkVerifyAndCleanWorker(b *testing.B) {
	tempDir := b.TempDir()
	fileChan := make(chan FileRecord, b.N)
	stats := &BenchmarkStats{StartTime: time.Now()}

	// Pre-create files
	for i := 0; i < b.N; i++ {
		testData := make([]byte, 1024*1024)
		rand.Read(testData)

		hasher := sha512.New()
		hasher.Write(testData)
		originalHash := hasher.Sum(nil)

		encodedContent := base64.StdEncoding.EncodeToString(testData)

		// Compress the encoded content with gzip
		var compressedBuf bytes.Buffer
		gzipWriter := gzip.NewWriter(&compressedBuf)
		gzipWriter.Write([]byte(encodedContent))
		gzipWriter.Close()

		filename := filepath.Join(tempDir, fmt.Sprintf("bench_file_%d.txt", i))
		os.WriteFile(filename, compressedBuf.Bytes(), 0644)

		fileChan <- FileRecord{
			Filename:     filename,
			OriginalHash: originalHash,
		}
	}
	close(fileChan)

	b.ResetTimer()
	b.ReportAllocs()

	var wg sync.WaitGroup
	wg.Add(1)
	go verifyAndCleanWorker(0, fileChan, stats, tempDir, &wg)
	wg.Wait()
}

func BenchmarkSHA512Hashing(b *testing.B) {
	data := make([]byte, 1024*1024) // 1MB
	rand.Read(data)

	b.ResetTimer()
	b.ReportAllocs()

	for i := 0; i < b.N; i++ {
		hasher := sha512.New()
		hasher.Write(data)
		_ = hasher.Sum(nil)
	}
}

func BenchmarkBase64Encoding(b *testing.B) {
	data := make([]byte, 1024*1024) // 1MB
	rand.Read(data)

	b.ResetTimer()
	b.ReportAllocs()

	for i := 0; i < b.N; i++ {
		_ = base64.StdEncoding.EncodeToString(data)
	}
}

func BenchmarkBase64Decoding(b *testing.B) {
	data := make([]byte, 1024*1024) // 1MB
	rand.Read(data)
	encodedData := base64.StdEncoding.EncodeToString(data)

	b.ResetTimer()
	b.ReportAllocs()

	for i := 0; i < b.N; i++ {
		_, _ = base64.StdEncoding.DecodeString(encodedData)
	}
}

func BenchmarkGzipCompression(b *testing.B) {
	data := make([]byte, 1024*1024) // 1MB
	rand.Read(data)

	b.ResetTimer()
	b.ReportAllocs()

	for i := 0; i < b.N; i++ {
		var buf bytes.Buffer
		gzipWriter := gzip.NewWriter(&buf)
		gzipWriter.Write(data)
		gzipWriter.Close()
	}
}

func BenchmarkGzipDecompression(b *testing.B) {
	data := make([]byte, 1024*1024) // 1MB
	rand.Read(data)

	// Pre-compress the data
	var buf bytes.Buffer
	gzipWriter := gzip.NewWriter(&buf)
	gzipWriter.Write(data)
	gzipWriter.Close()
	compressedData := buf.Bytes()

	b.ResetTimer()
	b.ReportAllocs()

	for i := 0; i < b.N; i++ {
		gzipReader, _ := gzip.NewReader(bytes.NewReader(compressedData))
		io.ReadAll(gzipReader)
		gzipReader.Close()
	}
}

func BenchmarkFullPipeline(b *testing.B) {
	tempDir := b.TempDir()
	config := Config{
		NumFiles:        b.N,
		FileSizeMB:      1,
		ConsumerWorkers: 4,
		TempDir:         tempDir,
		ShowProgress:    false,
	}

	b.ResetTimer()
	b.ReportAllocs()

	if err := runBenchmark(config); err != nil {
		b.Fatalf("Benchmark failed: %v", err)
	}
}
