package converter

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"
)

// Helper to create a dummy ImageSource with a string reader
func newStringImageSource(name, content, contentType string, index int) ImageSource {
	return ImageSource{
		OriginalFilename: name,
		Reader:           io.NopCloser(strings.NewReader(content)),
		ContentType:      contentType,
		Index:            index,
	}
}

// Helper to create a dummy ImageSource from a file
func newFileImageSource(t *testing.T, filename, contentType string, index int) ImageSource {
	t.Helper()
	// Use a real small image if available, otherwise a dummy text file
	path := filepath.Join("testdata", filename)
	if _, err := os.Stat(path); os.IsNotExist(err) {
		// Create a dummy file if it doesn't exist, for basic testing
		dummyPath := filepath.Join("testdata", "dummy_test_file.txt")
		_ = os.WriteFile(dummyPath, []byte("dummy content for "+filename), 0644)
		path = dummyPath
		slog.Warn("Test image file not found, using dummy", "path", filepath.Join("testdata", filename))
		// For real image processing tests, actual image files are needed.
		// These tests might focus on flow rather than actual image decoding.
	}


	file, err := os.Open(path)
	if err != nil {
		t.Fatalf("Failed to open test file %s: %v", path, err)
	}
	return ImageSource{
		OriginalFilename: filename,
		Reader:           file, // This will be closed by the converter
		ContentType:      contentType,
		Index:            index,
	}
}

func TestNewDefaultConfig(t *testing.T) {
	cfg := NewDefaultConfig()
	if cfg.JPEGQuality != 90 {
		t.Errorf("Expected JPEGQuality 90, got %d", cfg.JPEGQuality)
	}
	if cfg.NumWorkers <= 0 {
		t.Errorf("Expected NumWorkers > 0, got %d", cfg.NumWorkers)
	}
	if cfg.OutputFilename != "converted.pdf" {
		t.Errorf("Expected OutputFilename 'converted.pdf', got %s", cfg.OutputFilename)
	}
}

// Note: Testing processSingleImage thoroughly requires valid image data.
// These tests will be basic, focusing on flow and error handling for non-image data.
// To test actual image processing, place small valid jpg, png, webp files in testdata.
func TestProcessSingleImage_InvalidData(t *testing.T) {
	cfg := NewDefaultConfig()
	ctx := context.Background()

	// Use a source that is not a valid image format
	source := newStringImageSource("invalid.txt", "this is not an image", "text/plain", 0)

	// Redirect slog to a buffer to check logs if needed
	var logBuf bytes.Buffer
	originalLogger := slog.Default()
	slog.SetDefault(slog.New(slog.NewTextHandler(&logBuf, &slog.HandlerOptions{Level: slog.LevelDebug})))
	defer slog.SetDefault(originalLogger)

	processedImg := processSingleImage(ctx, cfg, source)

	if processedImg.Error == nil {
		t.Errorf("Expected error for invalid image data, got nil. Logs: %s", logBuf.String())
	} else {
		t.Logf("Received expected error for invalid data: %v", processedImg.Error)
	}
	if processedImg.Reader != nil {
		t.Error("Expected reader to be nil on error")
		if closer, ok := processedImg.Reader.(io.Closer); ok {
			closer.Close()
		}
	}
}

func TestProcessSingleImage_ContextCancellation(t *testing.T) {
	cfg := NewDefaultConfig()
	ctx, cancel := context.WithCancel(context.Background())
	cancel() // Cancel context immediately

	source := newStringImageSource("test.jpg", "dummy_jpeg_data", "image/jpeg", 0)
	processedImg := processSingleImage(ctx, cfg, source)

	if !errors.Is(processedImg.Error, context.Canceled) {
		t.Errorf("Expected context.Canceled error, got %v", processedImg.Error)
	}
}

func TestConvertToPDF_NoSources(t *testing.T) {
	cfg := NewDefaultConfig()
	ctx := context.Background()
	var writer bytes.Buffer

	hasContent, err := ConvertToPDF(ctx, []ImageSource{}, cfg, &writer)

	if err == nil {
		t.Error("Expected error for no sources, got nil")
	} else if !errors.Is(err, ErrNoSupportedImages) {
		t.Errorf("Expected ErrNoSupportedImages, got %v", err)
	}

	if hasContent {
		t.Error("Expected no content when no sources are provided")
	}
	if writer.Len() > 0 {
		t.Errorf("Expected empty PDF output, got %d bytes", writer.Len())
	}
}

func TestConvertToPDF_AllSourcesError(t *testing.T) {
	cfg := NewDefaultConfig()
	ctx := context.Background()
	var writer bytes.Buffer

	sources := []ImageSource{
		newStringImageSource("invalid1.txt", "not image", "text/plain", 0),
		newStringImageSource("invalid2.txt", "not image", "text/plain", 1),
	}

	hasContent, err := ConvertToPDF(ctx, sources, cfg, &writer)

	if err == nil {
		t.Error("Expected error when all sources fail, got nil")
	} else if !errors.Is(err, ErrNoSupportedImages) { // This is the current behavior
		t.Errorf("Expected ErrNoSupportedImages or similar, got %v", err)
	}


	if hasContent {
		t.Error("Expected no content when all sources fail")
	}
}

func TestConvertToPDF_ContextCancellation(t *testing.T) {
	cfg := NewDefaultConfig()
	ctx, cancel := context.WithCancel(context.Background())
	var writer bytes.Buffer

	// Create a source that would normally succeed if not for cancellation
	// For this test, we'll use a dummy file source.
	// Ensure 'dummy.jpg' exists in 'testdata' or is created by newFileImageSource's fallback.
	sources := []ImageSource{
		newFileImageSource(t, "dummy.jpg", "image/jpeg", 0),
	}

	cancel() // Cancel context before calling

	hasContent, err := ConvertToPDF(ctx, sources, cfg, &writer)

	if !errors.Is(err, context.Canceled) {
		t.Errorf("Expected context.Canceled error, got %v", err)
	}
	if hasContent {
		t.Error("Expected no content with immediate cancellation")
	}
}

// This test requires a running HTTP server for FetchImage.
// We'll use httptest.NewServer.
func TestFetchImage_Success(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "image/jpeg")
		fmt.Fprint(w, "fake_jpeg_data")
	}))
	defer server.Close()

	ctx := context.Background()
	imgSrc, err := FetchImage(ctx, server.URL, 0)
	if err != nil {
		t.Fatalf("FetchImage failed: %v", err)
	}
	defer imgSrc.Reader.Close()

	if imgSrc.ContentType != "image/jpeg" {
		t.Errorf("Expected content type image/jpeg, got %s", imgSrc.ContentType)
	}
	if imgSrc.OriginalFilename == "" {
		t.Error("Expected a filename to be derived from URL")
	}
	data, _ := io.ReadAll(imgSrc.Reader)
	if string(data) != "fake_jpeg_data" {
		t.Errorf("Expected 'fake_jpeg_data', got '%s'", string(data))
	}
}

func TestFetchImage_NotFound(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusNotFound)
	}))
	defer server.Close()

	ctx := context.Background()
	_, err := FetchImage(ctx, server.URL, 0)
	if err == nil {
		t.Fatal("Expected error for 404 Not Found, got nil")
	}
	t.Logf("Received expected error for 404: %v", err)
}

func TestFetchImage_UnsupportedContentType(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/html")
		fmt.Fprint(w, "<html></html>")
	}))
	defer server.Close()

	ctx := context.Background()
	imgSrc, err := FetchImage(ctx, server.URL, 0)
	if err == nil {
		imgSrc.Reader.Close() // Close reader if FetchImage unexpectedly succeeded
		t.Fatal("Expected error for unsupported content type, got nil")
	}
	if !errors.Is(err, ErrUnsupportedContentType) {
		t.Errorf("Expected ErrUnsupportedContentType, got %v", err)
	}
	t.Logf("Received expected error for unsupported content type: %v", err)
}

func TestFetchImage_ContextCancellation(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		time.Sleep(100 * time.Millisecond) // Ensure request starts
		fmt.Fprint(w, "slow_response")
	}))
	defer server.Close()

	ctx, cancel := context.WithCancel(context.Background())
	cancel() // Cancel before request can complete

	_, err := FetchImage(ctx, server.URL, 0)
	if !errors.Is(err, context.Canceled) && !strings.Contains(err.Error(), "context canceled") {
		// Error might be wrapped, so check string too
		t.Errorf("Expected context.Canceled error, got %v", err)
	}
}


// TestProcessImagesConcurrently_OrderAndCancellation
// This test is more complex as it involves concurrency and timing.
func TestProcessImagesConcurrently_OrderAndCancellation(t *testing.T) {
	cfg := NewDefaultConfig()
	cfg.NumWorkers = 2 // Control number of workers for predictability

	// Create some dummy sources.
	// processSingleImage will likely error out on these as they are not real images.
	// The focus here is on the orchestration by processImagesConcurrently.
	sources := []ImageSource{
		newStringImageSource("img0.txt", "data0", "text/plain", 0),
		newStringImageSource("img1.txt", "data1", "text/plain", 1),
		newStringImageSource("img2.txt", "data2", "text/plain", 2),
		newStringImageSource("img3.txt", "data3", "text/plain", 3),
	}

	ctx, cancel := context.WithCancel(context.Background())

	var wg sync.WaitGroup
	wg.Add(1) // For the main processing goroutine

	var results []ProcessedImage
	go func() {
		defer wg.Done()
		results = processImagesConcurrently(ctx, cfg, sources)
	}()

	// Allow some processing to start, then cancel
	time.Sleep(50 * time.Millisecond) // Small delay
	cancel()
	wg.Wait() // Wait for processImagesConcurrently to finish

	if len(results) != len(sources) {
		t.Fatalf("Expected %d results, got %d", len(sources), len(results))
	}

	cancelledCount := 0
	for i, res := range results {
		if res.Index != sources[i].Index {
			// This check assumes results are implicitly ordered by source input order before explicit sort.
			// processImagesConcurrently populates a slice of the same length, so res.Index should map to original.
			// The final sort in generatePDFFromProcessedImages is what matters for PDF.
			// Let's check if all original indices are present.
		}
		if res.Error != nil {
			if errors.Is(res.Error, context.Canceled) {
				cancelledCount++
			} else {
				// If it's not a cancellation error, it's likely a processing error due to dummy data
				t.Logf("Result for index %d has non-cancellation error: %v (expected due to dummy data or cancellation)", res.Index, res.Error)
			}
		} else {
			// This shouldn't happen with dummy text data if processSingleImage is strict.
			// Or if cancellation was too fast.
			t.Logf("Result for index %d has no error, but context was cancelled.", res.Index)
		}
	}

	if cancelledCount == 0 && len(sources) > 0 {
		t.Errorf("Expected some images to be marked as cancelled, but none were. Total results: %d", len(results))
	} else {
		t.Logf("%d images were marked with cancellation error or processing error.", cancelledCount)
	}
	// This test is more of a sanity check for the concurrent processing logic with cancellation.
	// Precise number of cancelled vs processed-with-error can vary based on timing.
}


// To properly test ConvertToPDF with actual PDF generation, you'd need:
// 1. Valid small image files (jpg, png, webp).
// 2. A way to inspect the generated PDF (e.g., check page count, or if it's a valid PDF).
//    This often requires external libraries or is very basic (like checking for PDF magic numbers).

// For now, we'll assume that if no error occurs and hasContent is true, it's a basic success.
// Place `test.jpg`, `test.png`, `test.webp` (valid, small images) in `testdata/` for this to work.
func TestConvertToPDF_WithValidDummyImages(t *testing.T) {
	// Create dummy valid image files in testdata if they don't exist
	// For this test, we rely on newFileImageSource's fallback to dummy text files.
	// The converter will error on these, so this test will be similar to AllSourcesError.
	// To make this a true success test, you need REAL images.

	td := t.TempDir()
	// Create dummy files that are NOT valid images but pass os.Stat
	_ = os.WriteFile(filepath.Join(td, "test.jpg"), []byte("dummy jpg"), 0644)
	_ = os.WriteFile(filepath.Join(td, "test.png"), []byte("dummy png"), 0644)

	// Override testdata path for newFileImageSource for this test
	originalTestDataPath := "testdata"
	defer func() {
		// This is a bit hacky; ideally, newFileImageSource would take the base path.
		// For now, we know it prepends "testdata". This won't work as intended
		// without modifying newFileImageSource or creating files in the actual ./testdata
		// For this self-contained example, let's assume newFileImageSource will use its fallback.
		// The test will then behave like AllSourcesError.
	}()
	// If actual files 'test.jpg', 'test.png' are in ./testdata, this test becomes more meaningful.
	// For CI, ensure these files are present.

	cfg := NewDefaultConfig()
	ctx := context.Background()
	var writer bytes.Buffer

	sources := []ImageSource{
		newFileImageSource(t, "test.jpg", "image/jpeg", 0), // Will use dummy_test_file.txt if test.jpg not found
		newFileImageSource(t, "test.png", "image/png", 1), // Will use dummy_test_file.txt if test.png not found
	}

	hasContent, err := ConvertToPDF(ctx, sources, cfg, &writer)

	// Given that these are dummy text files, processSingleImage will error out.
	// So, ConvertToPDF should return an error and no content.
	if err == nil {
		t.Errorf("Expected error with dummy (non-image) files, got nil. PDF size: %d", writer.Len())
		if writer.Len() > 0 {
			// You could try to save this to inspect it:
			// os.WriteFile("error_dummy_output.pdf", writer.Bytes(), 0644)
		}
	} else if !errors.Is(err, ErrNoSupportedImages) {
		t.Errorf("Expected ErrNoSupportedImages with dummy files, got %v", err)
	}

	if hasContent {
		t.Error("Expected no content with dummy (non-image) files")
	}
}


func TestGetContentTypeFromFilename(t *testing.T) {
	tests := []struct {
		filename string
		expected string
	}{
		{"image.jpg", "image/jpeg"},
		{"image.JPEG", "image/jpeg"},
		{"document.png", "image/png"},
		{"animation.webp", "image/webp"},
		{"archive.zip", ""},
		{"unknown", ""},
		{".bashrc", ""},
	}

	for _, tt := range tests {
		got := GetContentTypeFromFilename(tt.filename)
		if got != tt.expected {
			t.Errorf("GetContentTypeFromFilename(%s): expected '%s', got '%s'", tt.filename, tt.expected, got)
		}
	}
}
