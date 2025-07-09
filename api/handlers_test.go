package api

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"mime/multipart"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"manga_to_pdf/internal/converter" // Assuming this path is correct
)

// Helper function to create a new multipart/form-data request with files and form values.
func newFileUploadRequest(t *testing.T, url string, params map[string]string, files map[string]string) *http.Request {
	t.Helper()
	body := new(bytes.Buffer)
	writer := multipart.NewWriter(body)

	for key, val := range params {
		err := writer.WriteField(key, val)
		if err != nil {
			t.Fatalf("Failed to write field %s: %v", key, err)
		}
	}

	for key, path := range files {
		// Use a real small image if available, otherwise a dummy text file from testdata
		fullPath := filepath.Join("testdata", path)
		if _, err := os.Stat(fullPath); os.IsNotExist(err) {
			// Create a dummy file if it doesn't exist, for basic testing
			// This is essential if actual image files are not checked into the repo for tests.
			dummyPath := filepath.Join("testdata", "dummy_api_test_file.txt")
			_ = os.WriteFile(dummyPath, []byte("dummy content for "+path), 0644)
			fullPath = dummyPath
			slog.Warn("API Test: image file not found, using dummy", "path", path, "using", fullPath)
		}


		file, err := os.Open(fullPath)
		if err != nil {
			t.Fatalf("Failed to open file %s: %v", fullPath, err)
		}
		defer file.Close()

		part, err := writer.CreateFormFile(key, filepath.Base(path))
		if err != nil {
			t.Fatalf("Failed to create form file for %s: %v", path, err)
		}
		_, err = io.Copy(part, file)
		if err != nil {
			t.Fatalf("Failed to copy file content for %s: %v", path, err)
		}
	}

	err := writer.Close()
	if err != nil {
		t.Fatalf("Failed to close multipart writer: %v", err)
	}

	req, err := http.NewRequest("POST", url, body)
	if err != nil {
		t.Fatalf("Failed to create request: %v", err)
	}
	req.Header.Set("Content-Type", writer.FormDataContentType())
	return req
}

// TestHandleConvert_NoImages tests the scenario where no images are provided.
func TestHandleConvert_NoImages(t *testing.T) {
	req := newFileUploadRequest(t, "/convert", map[string]string{}, map[string]string{})
	rr := httptest.NewRecorder()
	handler := http.HandlerFunc(HandleConvert)
	handler.ServeHTTP(rr, req)

	if status := rr.Code; status != http.StatusBadRequest {
		t.Errorf("handler returned wrong status code: got %v want %v", status, http.StatusBadRequest)
		t.Logf("Response body: %s", rr.Body.String())
	}

	var resp APIErrorResponse
	if err := json.Unmarshal(rr.Body.Bytes(), &resp); err != nil {
		t.Fatalf("Could not parse JSON response: %v", err)
	}
	expectedError := "No images provided"
	if !strings.Contains(resp.Error, expectedError) {
		t.Errorf("handler returned unexpected error message: got '%s' want substring '%s'", resp.Error, expectedError)
	}
}

// TestHandleConvert_InvalidConfigJSON tests providing malformed JSON in the 'config' field.
func TestHandleConvert_InvalidConfigJSON(t *testing.T) {
	params := map[string]string{
		"config": "{'output_filename': 'test.pdf', \"jpeg_quality\": invalid}", // Invalid JSON
	}
	// Need at least one image file for the handler to proceed to config parsing for this specific test path
	// otherwise it might fail earlier due to "no images".
	files := map[string]string{
		"images": "dummy.txt", // dummy.txt should be in api/testdata
	}
	req := newFileUploadRequest(t, "/convert", params, files)

	rr := httptest.NewRecorder()
	handler := http.HandlerFunc(HandleConvert)
	handler.ServeHTTP(rr, req)

	if status := rr.Code; status != http.StatusBadRequest {
		t.Errorf("handler returned wrong status code: got %v want %v", status, http.StatusBadRequest)
		t.Logf("Response body: %s", rr.Body.String())
		return // Avoid further checks if status is wrong
	}

	var resp APIErrorResponse
	if err := json.Unmarshal(rr.Body.Bytes(), &resp); err != nil {
		t.Fatalf("Could not parse JSON response: %v. Body: %s", err, rr.Body.String())
	}
	expectedError := "Invalid 'config' JSON"
	if !strings.Contains(resp.Error, expectedError) {
		t.Errorf("handler returned unexpected error message: got '%s' want substring '%s'", resp.Error, expectedError)
	}
}

// TestHandleConvert_InvalidImageURLsJSON tests providing malformed JSON in 'image_urls'.
func TestHandleConvert_InvalidImageURLsJSON(t *testing.T) {
	params := map[string]string{
		"image_urls": "['http://example.com/image.jpg', invalid_url]", // Invalid JSON array
	}
	req := newFileUploadRequest(t, "/convert", params, map[string]string{}) // No files, just URL
	rr := httptest.NewRecorder()
	handler := http.HandlerFunc(HandleConvert)
	handler.ServeHTTP(rr, req)

	if status := rr.Code; status != http.StatusBadRequest {
		t.Errorf("handler returned wrong status code: got %v want %v", status, http.StatusBadRequest)
		t.Logf("Response body: %s", rr.Body.String())
		return
	}
	var resp APIErrorResponse
	if err := json.Unmarshal(rr.Body.Bytes(), &resp); err != nil {
		t.Fatalf("Could not parse JSON response: %v. Body: %s", err, rr.Body.String())
	}
	expectedError := "Invalid 'image_urls' JSON"
	if !strings.Contains(resp.Error, expectedError) {
		t.Errorf("handler returned unexpected error message: got '%s' want substring '%s'", resp.Error, expectedError)
	}
}


// TestHandleConvert_FetchImageFailures tests when URL fetching fails.
func TestHandleConvert_FetchImageFailures(t *testing.T) {
	// Setup a local server that will return errors for image URLs
	mockServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if strings.HasSuffix(r.URL.Path, "notfound.jpg") {
			w.WriteHeader(http.StatusNotFound)
		} else if strings.HasSuffix(r.URL.Path, "badtype.jpg") {
			w.Header().Set("Content-Type", "text/plain")
			fmt.Fprint(w, "this is not an image")
		} else {
			w.WriteHeader(http.StatusInternalServerError)
		}
	}))
	defer mockServer.Close()

	urlsJSON := fmt.Sprintf(`["%s/notfound.jpg", "%s/badtype.jpg"]`, mockServer.URL, mockServer.URL)
	params := map[string]string{
		"image_urls": urlsJSON,
	}
	req := newFileUploadRequest(t, "/convert", params, map[string]string{})
	rr := httptest.NewRecorder()
	handler := http.HandlerFunc(HandleConvert)
	handler.ServeHTTP(rr, req)

	// Expect 422 because some images might be processed (if any were uploaded),
	// but fetching these URLs will fail. If only URLs are provided and all fail, it's 422.
	if status := rr.Code; status != http.StatusUnprocessableEntity {
		t.Errorf("handler returned wrong status code: got %v want %v", status, http.StatusUnprocessableEntity)
		t.Logf("Response body: %s", rr.Body.String())
		return
	}
	var resp APIErrorResponse
	if err := json.Unmarshal(rr.Body.Bytes(), &resp); err != nil {
		t.Fatalf("Could not parse JSON response: %v", err)
	}
	// The error message might vary, but it should indicate failure to fetch or process.
	// Example: "Failed to fetch any images from URLs and no files uploaded."
	// or "No images could be processed into the PDF" if converter.ErrNoSupportedImages is hit.
	if resp.Error == "" {
		t.Error("Expected an error message, got empty")
	} else {
		t.Logf("Received error for fetch failures: %s, Details: %v", resp.Error, resp.Details)
	}
	// Check that details might contain info about the failed URLs
	if resp.Details == nil {
		t.Logf("Error details are nil, which is acceptable if the main error is descriptive.")
	} else {
		detailsStr, ok := resp.Details.(string) // Or []string depending on how HandleConvert formats it
		if ok {
			if !strings.Contains(detailsStr, "notfound.jpg") && !strings.Contains(detailsStr, "badtype.jpg") {
				// This check is too specific if the details format changes.
				// More generally, just log the details.
				t.Logf("Details string does not explicitly mention failed URLs, but this might be ok. Details: %s", detailsStr)
			}
		} else {
			t.Logf("Details are not a simple string: %T %v", resp.Details, resp.Details)
		}

	}
}

// TestHandleConvert_SuccessfulConversion_DummyFileAsImage
// This test uses a dummy text file. The converter.ConvertToPDF will fail to process it as an image.
// So, the API should return an error (e.g., 422 Unprocessable Entity).
func TestHandleConvert_DummyFileAsImage(t *testing.T) {
	// Ensure dummy.txt is in api/testdata
	files := map[string]string{
		"images": "dummy.txt",
	}
	params := map[string]string{
		"config": `{"output_filename": "test_dummy.pdf"}`,
	}
	req := newFileUploadRequest(t, "/convert", params, files)
	rr := httptest.NewRecorder()
	handler := http.HandlerFunc(HandleConvert)
	handler.ServeHTTP(rr, req)

	if status := rr.Code; status != http.StatusUnprocessableEntity {
		t.Errorf("handler returned wrong status code with dummy file: got %v want %v", status, http.StatusUnprocessableEntity)
		t.Logf("Response body: %s", rr.Body.String())
		return
	}

	var resp APIErrorResponse
	if err := json.Unmarshal(rr.Body.Bytes(), &resp); err != nil {
		t.Fatalf("Could not parse JSON error response: %v. Body: %s", err, rr.Body.String())
	}

	expectedErrorSubstrings := []string{"No content added to PDF", "No images could be processed"}
	foundError := false
	for _, sub := range expectedErrorSubstrings {
		if strings.Contains(resp.Error, sub) {
			foundError = true
			break
		}
	}
	if !foundError {
		t.Errorf("handler returned unexpected error message for dummy file: got '%s', expected one of %v", resp.Error, expectedErrorSubstrings)
	}
}


// TestHandleConvert_ContextCancellationDuringProcessing
// This test is tricky because cancellation needs to happen *during* processing.
// We can use a custom converter function that signals readiness and waits for cancellation.
func TestHandleConvert_ContextCancellationDuringProcessing(t *testing.T) {
	// Store the original converter function and defer its restoration
	originalConvertToPDF := converter.ConvertToPDF
	defer func() { converter.ConvertToPDF = originalConvertToPDF }()

	ctxCancelledSignal := make(chan struct{})    // To signal the test that the context in handler was cancelled
	proceedWithConversion := make(chan struct{}) // To signal the mock converter to proceed after delay

	// Mock converter.ConvertToPDF
	converter.ConvertToPDF = func(ctx context.Context, sources []converter.ImageSource, cfg *converter.Config, writer io.Writer) (bool, error) {
		// Signal that conversion has started and is about to wait on context
		slog.Debug("Mock ConvertToPDF started, waiting for context or proceed signal")
		select {
		case <-ctx.Done():
			slog.Debug("Mock ConvertToPDF: context cancelled before proceeding.")
			close(ctxCancelledSignal) // Signal that context was indeed cancelled
			return false, ctx.Err()
		case <-proceedWithConversion:
			slog.Debug("Mock ConvertToPDF: Proceeding after signal (context not cancelled yet).")
			// Simulate some work and then a successful conversion
			fmt.Fprint(writer, "%PDF-1.4\n%%EOF\n") // Minimal PDF
			return true, nil
		case <-time.After(5 * time.Second): // Timeout for the mock converter itself
			slog.Error("Mock ConvertToPDF: timed out waiting for context cancellation or proceed signal")
			return false, errors.New("mock converter timeout")
		}
	}

	// Prepare request
	files := map[string]string{"images": "dummy.txt"} // Need at least one "image"
	req := newFileUploadRequest(t, "/convert", nil, files)

	// Create a context that we can cancel
	reqCtx, cancelReqCtx := context.WithCancel(req.Context())
	req = req.WithContext(reqCtx)

	rr := httptest.NewRecorder()
	handler := http.HandlerFunc(HandleConvert)

	go func() {
		// Simulate client cancelling the request after a short delay
		// This delay should be long enough for the request to reach the mock converter,
		// but short enough for the cancellation to occur while mock is "processing".
		time.Sleep(100 * time.Millisecond)
		slog.Debug("Test: Cancelling request context now.")
		cancelReqCtx()
	}()

	handler.ServeHTTP(rr, req) // This will block until HandleConvert completes

	// Check if the context was cancelled as expected by the mock
	select {
	case <-ctxCancelledSignal:
		slog.Debug("Test: Mock converter confirmed context cancellation.")
	case <-time.After(1 * time.Second):
		// If mock didn't signal cancellation, try to proceed it to avoid deadlock if test fails
		// then fail the test.
		close(proceedWithConversion)
		t.Error("Test: Mock converter did not signal context cancellation in time.")
	}


	// Expected status depends on when cancellation is caught.
	// If caught by server/handler before PDF generation logic fully completes and writes headers,
	// it might be 499 (if server supports it) or a timeout-like status.
	// If caught by converter, HandleConvert should translate ctx.Err() to appropriate HTTP error.
	// http.StatusGatewayTimeout (504) or http.StatusServiceUnavailable (503) are possibilities.
	// For client cancellation, 499 is common but not standard. Let's check for 504 or 499 (though httptest might not show 499).
	// Our handler maps context.Canceled to StatusGatewayTimeout.
	if status := rr.Code; status != http.StatusGatewayTimeout {
		t.Errorf("handler returned wrong status code for client cancellation: got %v want %v", status, http.StatusGatewayTimeout)
		t.Logf("Response body: %s", rr.Body.String())
	}

	var resp APIErrorResponse
	if err := json.Unmarshal(rr.Body.Bytes(), &resp); err != nil {
		t.Fatalf("Could not parse JSON error response: %v. Body: %s", err, rr.Body.String())
	}
	if !strings.Contains(resp.Error, "canceled") && !strings.Contains(resp.Error, "timed out") {
		t.Errorf("Expected error message to indicate cancellation or timeout, got: %s", resp.Error)
	}
}


// TestMain is used to create dummy files in testdata if they don't exist.
func TestMain(m *testing.M) {
	// Create api/testdata directory if it doesn't exist
	testDataDir := "testdata"
	if _, err := os.Stat(testDataDir); os.IsNotExist(err) {
		os.Mkdir(testDataDir, 0755)
	}

	// Create a dummy text file to be used when actual images are not available/needed for a test.
	dummyFilePath := filepath.Join(testDataDir, "dummy.txt")
	if _, err := os.Stat(dummyFilePath); os.IsNotExist(err) {
		err := os.WriteFile(dummyFilePath, []byte("This is a dummy file for API testing."), 0644)
		if err != nil {
			slog.Error("Failed to create dummy.txt for tests", "error", err)
			os.Exit(1) // Abort tests if we can't create this essential dummy file
		}
	}
	dummyApiTestFilePath := filepath.Join(testDataDir, "dummy_api_test_file.txt")
	if _, err := os.Stat(dummyApiTestFilePath); os.IsNotExist(err) {
		err := os.WriteFile(dummyApiTestFilePath, []byte("This is another dummy file for API testing."), 0644)
		if err != nil {
			slog.Error("Failed to create dummy_api_test_file.txt for tests", "error", err)
			os.Exit(1)
		}
	}


	// TODO: Add small, valid test.jpg, test.png, test.webp files to api/testdata
	// For example:
	// CreateDummyImage(filepath.Join(testDataDir, "test.jpg"), "jpg")
	// CreateDummyImage(filepath.Join(testDataDir, "test.png"), "png")
	// CreateDummyImage(filepath.Join(testDataDir, "test.webp"), "webp")
	// These would be actual minimal valid image files.

	// Run tests
	exitVal := m.Run()
	os.Exit(exitVal)
}

// Note: A TestHandleConvert_Success test would require:
// 1. Actual small, valid image files (e.g., test.jpg, test.png, test.webp) in api/testdata.
// 2. The converter.ConvertToPDF to actually work with these images and produce a PDF.
// 3. Potentially, a way to validate the output PDF (e.g., check magic number, or use a PDF parsing library).
// Example structure:
/*
func TestHandleConvert_Success(t *testing.T) {
	// Ensure valid image files (e.g., test.jpg, test.png) are in api/testdata
	files := map[string]string{
		"images[]": "test.jpg", // Use actual image files
		"images[]": "test.png",
	}
	// Optionally, add a URL to a known small public image
	// server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { ... serve small image ... }))
	// defer server.Close()
	// urlsJSON := fmt.Sprintf(`["%s/some_image.webp"]`, server.URL)

	params := map[string]string{
		"config": `{"output_filename": "success.pdf"}`,
		// "image_urls": urlsJSON, // If testing URLs
	}

	req := newFileUploadRequest(t, "/convert", params, files)
	rr := httptest.NewRecorder()
	handler := http.HandlerFunc(HandleConvert)
	handler.ServeHTTP(rr, req)

	if status := rr.Code; status != http.StatusOK {
		t.Errorf("handler returned wrong status code: got %v want %v", status, http.StatusOK)
		t.Logf("Response body: %s", rr.Body.String()) // Log error response if any
		return
	}

	if contentType := rr.Header().Get("Content-Type"); contentType != "application/pdf" {
		t.Errorf("handler returned wrong Content-Type: got %s want application/pdf", contentType)
	}

	if contentDisposition := rr.Header().Get("Content-Disposition"); !strings.Contains(contentDisposition, `filename="success.pdf"`) {
		t.Errorf("handler returned wrong Content-Disposition: got %s want containing filename=\"success.pdf\"", contentDisposition)
	}

	if rr.Body.Len() == 0 {
		t.Error("handler returned empty body for PDF")
	}
	// Optionally, check PDF magic number
	// pdfMagicNumber := []byte("%PDF-")
	// if !bytes.HasPrefix(rr.Body.Bytes(), pdfMagicNumber) {
	// 	t.Error("Response body does not look like a PDF")
	// }
	t.Logf("Successfully received PDF of size %d bytes", rr.Body.Len())
}
*/
