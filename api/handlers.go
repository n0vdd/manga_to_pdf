package api

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"sort"
	"strconv"
	"strings"
	"sync" // For order preservation with fetched URLs

	"manga_to_pdf/internal/converter"
)

const defaultMaxMemory = 32 << 20 // 32 MB for multipart form parsing

type APIErrorResponse struct {
	Error   string      `json:"error"`
	Details interface{} `json:"details,omitempty"`
}

func writeJSONError(w http.ResponseWriter, message string, details interface{}, statusCode int) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(statusCode)
	errResponse := APIErrorResponse{
		Error:   message,
		Details: details,
	}
	if err := json.NewEncoder(w).Encode(errResponse); err != nil {
		slog.Error("Failed to write JSON error response", "error", err)
		// Fallback if JSON encoding fails
		http.Error(w, `{"error":"Failed to serialize error message"}`, http.StatusInternalServerError)
	}
}

// Helper struct to manage indexed image sources, especially when fetching URLs concurrently
type indexedImageSource struct {
	source converter.ImageSource
	err    error
}

func HandleConvert(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		writeJSONError(w, "Invalid request method", "Only POST is allowed", http.StatusMethodNotAllowed)
		return
	}

	ctx := r.Context()

	// Ensure body is closed
	defer func() {
		if r.Body != nil {
			io.Copy(io.Discard, r.Body) // Drain any remaining parts of the body
			r.Body.Close()
		}
	}()

	// Parse multipart form
	// The request body is an io.ReadCloser. It can be read once.
	// ParseMultipartForm reads the body.
	if err := r.ParseMultipartForm(defaultMaxMemory); err != nil {
		if err == io.EOF || err == io.ErrUnexpectedEOF { // These can happen if body is empty or malformed
			slog.Warn("Empty or malformed request body", "error", err)
			writeJSONError(w, "Malformed request body or empty request", err.Error(), http.StatusBadRequest)
			return
		}
		slog.Error("Failed to parse multipart form", "error", err)
		writeJSONError(w, "Failed to parse request data", err.Error(), http.StatusBadRequest)
		return
	}

	slog.Debug("Multipart form parsed successfully")

	// --- Configuration ---
	apiConfig := converter.NewDefaultConfig()
	configStr := r.FormValue("config")
	if configStr != "" {
		slog.Debug("Received config string", "config", configStr)
		if err := json.Unmarshal([]byte(configStr), apiConfig); err != nil {
			slog.Warn("Failed to parse 'config' JSON", "error", err, "configStr", configStr)
			writeJSONError(w, "Invalid 'config' JSON", err.Error(), http.StatusBadRequest)
			return
		}
		// Validate config values (JPEGQuality, NumWorkers)
		if apiConfig.JPEGQuality < 1 || apiConfig.JPEGQuality > 100 {
			slog.Warn("Invalid JPEG quality in config, using default", "provided", apiConfig.JPEGQuality)
			apiConfig.JPEGQuality = converter.NewDefaultConfig().JPEGQuality // Reset to default
		}
		if apiConfig.NumWorkers <= 0 {
			slog.Warn("Invalid NumWorkers in config, using default", "provided", apiConfig.NumWorkers)
			apiConfig.NumWorkers = converter.NewDefaultConfig().NumWorkers // Reset to default
		}
		slog.Debug("Successfully parsed config", "parsedConfig", apiConfig)
	} else {
		slog.Debug("No 'config' provided, using default config")
	}

	var imageSources []converter.ImageSource
	var sourceIndex int // To maintain original order

	// --- Process Uploaded Files ---
	// r.MultipartForm is populated by ParseMultipartForm.
	uploadedFiles := r.MultipartForm.File["images"]
	slog.Debug("Processing uploaded files", "count", len(uploadedFiles))
	for _, fileHeader := range uploadedFiles {
		slog.Debug("Processing uploaded file", "filename", fileHeader.Filename, "size", fileHeader.Size)
		file, err := fileHeader.Open()
		if err != nil {
			slog.Error("Failed to open uploaded file", "filename", fileHeader.Filename, "error", err)
			// Consider if one bad file should stop the whole process or just be skipped.
			// For now, let's try to continue with other files, but this one will be skipped.
			// To properly skip, we'd need to collect errors and report them.
			// For simplicity in this step, a single file error might cause a general failure.
			// A more robust approach would be to collect all sources and errors, then decide.
			writeJSONError(w, fmt.Sprintf("Failed to open uploaded file: %s", fileHeader.Filename), err.Error(), http.StatusInternalServerError)
			return // Early exit for now
		}
		// Note: The 'file' (multipart.File) needs to be closed. converter.processSingleImage will close it.

		contentType := fileHeader.Header.Get("Content-Type")
		if contentType == "" || contentType == "application/octet-stream" {
			// Fallback to extension if content type is generic or missing
			contentType = converter.GetContentTypeFromFilename(fileHeader.Filename)
			slog.Debug("Guessed content type from filename", "filename", fileHeader.Filename, "guessedType", contentType)
		}

		imageSources = append(imageSources, converter.ImageSource{
			OriginalFilename: fileHeader.Filename,
			Reader:           file, // This is an io.ReadCloser
			ContentType:      contentType,
			Index:            sourceIndex,
		})
		sourceIndex++
	}
	slog.Debug("Finished processing uploaded files", "count", len(imageSources))

	// --- Process Image URLs ---
	imageURLsStr := r.FormValue("image_urls")
	var fetchedSources []converter.ImageSource // To hold successfully fetched sources from URLs

	if imageURLsStr != "" {
		slog.Debug("Processing image_urls", "urls_string", imageURLsStr)
		var urls []string
		if err := json.Unmarshal([]byte(imageURLsStr), &urls); err != nil {
			slog.Warn("Failed to parse 'image_urls' JSON", "error", err, "urlsStr", imageURLsStr)
			// Close any already opened uploaded files before returning
			for _, src := range imageSources {
				if src.Reader != nil {
					src.Reader.Close()
				}
			}
			writeJSONError(w, "Invalid 'image_urls' JSON", err.Error(), http.StatusBadRequest)
			return
		}

		if len(urls) > 0 {
			slog.Debug("Fetching images from URLs", "count", len(urls))
			fetchedChan := make(chan indexedImageSource, len(urls))
			var wg sync.WaitGroup

			for _, urlStr := range urls {
				wg.Add(1)
				go func(u string, currentIndex int) {
					defer wg.Done()
					select {
					case <-ctx.Done():
						fetchedChan <- indexedImageSource{err: ctx.Err()}
						return
					default:
						slog.Debug("Fetching URL", "url", u, "index", currentIndex)
						imgSrc, err := converter.FetchImage(ctx, u, currentIndex) // Pass current global index
						if err != nil {
							slog.Warn("Failed to fetch image from URL", "url", u, "error", err)
							// Send error to channel, reader is already closed by FetchImage on error
							fetchedChan <- indexedImageSource{err: err, source: converter.ImageSource{OriginalFilename: u, Index: currentIndex}}
						} else {
							slog.Debug("Successfully fetched URL", "url", u, "filename", imgSrc.OriginalFilename)
							fetchedChan <- indexedImageSource{source: imgSrc}
						}
					}
				}(urlStr, sourceIndex) // Pass the current sourceIndex for this URL
				sourceIndex++ // Increment global index for each URL source
			}

			wg.Wait()
			close(fetchedChan)

			tempFetchedSources := make([]indexedImageSource, 0, len(urls))
			for res := range fetchedChan {
				tempFetchedSources = append(tempFetchedSources, res)
			}
			// Sort by original index to maintain order relative to other URLs
			sort.Slice(tempFetchedSources, func(i, j int) bool {
				return tempFetchedSources[i].source.Index < tempFetchedSources[j].source.Index
			})

			urlErrors := []string{}
			for _, res := range tempFetchedSources {
				if res.err != nil {
					// Collect errors for URLs. Decide if one failure means total failure.
					// For now, collect and log. If an error occurs, the source.Reader will be nil or closed.
					urlErrors = append(urlErrors, fmt.Sprintf("Failed to fetch %s: %s", res.source.OriginalFilename, res.err.Error()))
					// Ensure reader is closed if somehow it wasn't (FetchImage should handle this)
					if res.source.Reader != nil {
						res.source.Reader.Close()
					}
				} else if res.source.Reader != nil { // Only add if successfully fetched and reader is present
					fetchedSources = append(fetchedSources, res.source)
				}
			}

			if len(urlErrors) > 0 && len(fetchedSources) == 0 && len(uploadedFiles) == 0 {
				// All URL fetches failed, and no uploaded files either
				slog.Warn("All image URL fetches failed and no uploaded files.", "errors", strings.Join(urlErrors, "; "))
				// Close any uploaded file readers if they existed but fetchedSources is the only source type
				for _, src := range imageSources { // imageSources here only contains uploaded files
					if src.Reader != nil {
						src.Reader.Close()
					}
				}
				writeJSONError(w, "Failed to fetch any images from URLs and no files uploaded.", urlErrors, http.StatusUnprocessableEntity)
				return
			}
			// Log URL errors if any, but proceed if some images were fetched or uploaded
			if len(urlErrors) > 0 {
				slog.Warn("Some image URL fetches failed", "errors", strings.Join(urlErrors, "; "))
			}
		}
	}
	// Append successfully fetched URL sources to the main list
	imageSources = append(imageSources, fetchedSources...)
	slog.Debug("Finished processing image_urls", "successfully_fetched_count", len(fetchedSources))

	// --- Final Check and Cleanup ---
	if len(imageSources) == 0 {
		slog.Info("No image files or URLs provided or successfully processed up to this point.")
		writeJSONError(w, "No images provided", "Please upload files or provide image URLs.", http.StatusBadRequest)
		return
	}

	// Ensure sources are sorted by their original index before passing to converter
	sort.SliceStable(imageSources, func(i, j int) bool {
		return imageSources[i].Index < imageSources[j].Index
	})

	// Log the final list of sources being sent to the converter
	for idx, src := range imageSources {
		slog.Debug("Source for conversion", "final_list_index", idx, "original_index", src.Index, "filename", src.OriginalFilename, "has_reader", src.Reader != nil, "url", src.URL)
	}

	// --- Conversion ---
	var pdfOutputBuffer bytes.Buffer
	slog.Info("Starting PDF conversion with converter package", "num_sources", len(imageSources), "config", apiConfig)

	// The readers in imageSources (from uploads or FetchImage) will be closed by the converter package.
	hasContent, err := converter.ConvertToPDF(ctx, imageSources, apiConfig, &pdfOutputBuffer)
	if err != nil {
		slog.Error("PDF conversion failed", "error", err)
		// imageSources readers should have been closed by ConvertToPDF or its sub-functions
		if errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) {
			writeJSONError(w, "PDF conversion timed out or was canceled by client", err.Error(), http.StatusGatewayTimeout) // Or 499 Client Closed Request if detectable
		} else if errors.Is(err, converter.ErrNoSupportedImages) {
			writeJSONError(w, "No images could be processed into the PDF", err.Error(), http.StatusUnprocessableEntity)
		} else if errors.Is(err, converter.ErrUnsupportedContentType) {
			writeJSONError(w, "Unsupported image content type from URL", err.Error(), http.StatusUnprocessableEntity)
		} else {
			writeJSONError(w, "Failed to convert images to PDF", err.Error(), http.StatusInternalServerError)
		}
		return
	}

	if !hasContent {
		slog.Info("Conversion successful but PDF has no content (e.g., all images were invalid or skipped).")
		writeJSONError(w, "No content added to PDF", "All provided images might have been invalid, corrupted, or unsupported.", http.StatusUnprocessableEntity)
		return
	}

	// --- Success Response ---
	outputFilename := apiConfig.OutputFilename
	if outputFilename == "" {
		outputFilename = "converted.pdf"
	}
	// Sanitize filename slightly (very basic)
	outputFilename = strings.ReplaceAll(outputFilename, "/", "_")
	outputFilename = strings.ReplaceAll(outputFilename, "\"", "")
	if !strings.HasSuffix(strings.ToLower(outputFilename), ".pdf") {
		outputFilename += ".pdf"
	}

	w.Header().Set("Content-Type", "application/pdf")
	w.Header().Set("Content-Disposition", fmt.Sprintf(`attachment; filename="%s"`, outputFilename))
	contentLength := pdfOutputBuffer.Len()
	w.Header().Set("Content-Length", strconv.Itoa(contentLength))

	slog.Info("Successfully generated PDF", "filename", outputFilename, "size", contentLength)
	if _, err := pdfOutputBuffer.WriteTo(w); err != nil {
		// This error usually means the client closed the connection.
		slog.Error("Failed to write PDF to response", "error", err)
		// Cannot send JSON error here as headers are already sent.
	}
}
