package converter

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"image"
	_ "image/jpeg" // Added for JPEG decoding (register decoder)
	_ "image/png"  // Added for PNG encoding (register decoder)
	"io"
	"log/slog"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"runtime"
	"sort"
	"strings"
	"sync"

	"github.com/disintegration/imaging"
	"github.com/jung-kurt/gofpdf"
	_ "golang.org/x/image/webp" // Added for WebP decoding (register decoder)
)

// bufferPool is used to reuse byte buffers for WEBP to JPG conversion.
var bufferPool = sync.Pool{
	New: func() interface{} {
		return new(bytes.Buffer)
	},
}

// ErrNoSupportedImages is returned when no supported image sources are provided or processed.
var ErrNoSupportedImages = errors.New("no supported images were successfully processed")

// ErrUnsupportedContentType is returned when an image URL points to an unsupported content type.
var ErrUnsupportedContentType = errors.New("unsupported content type from URL")

// ImageSource represents a single image to be processed.
// It can be an uploaded file (via io.ReadCloser) or a URL (string).
type ImageSource struct {
	OriginalFilename string        // Original filename from upload or derived from URL
	Reader           io.ReadCloser // Reader for the image data
	URL              string        // URL if the image is to be fetched
	ContentType      string        // Detected content type (e.g., "image/jpeg", "image/png", "image/webp")
	Index            int           // Original index for ordering
}

// ProcessedImage holds the data for an image that has been processed and is ready for PDF registration.
type ProcessedImage struct {
	Index            int       // Original index of the file, for ordering
	OriginalFilename string    // Original filename
	Error            error     // Error encountered during processing
	Reader           io.Reader // Reader for image data (either *os.File or *bytes.Buffer)
	Width            float64   // Width of the image in points
	Height           float64   // Height of the image in points
	ImageTypeForPDF  string    // Type string for gofpdf ("PNG", "JPG")
}

// Config holds configuration for the conversion process.
type Config struct {
	JPEGQuality    int
	NumWorkers     int
	OutputFilename string // Suggested output filename, used for Content-Disposition
	// InputDirectory is no longer needed here as images come from ImageSource list
}

// NewDefaultConfig creates a new Config with default values.
func NewDefaultConfig() *Config {
	return &Config{
		JPEGQuality:    90,
		NumWorkers:     runtime.NumCPU(),
		OutputFilename: "converted.pdf",
	}
}

// processSingleImage processes a single ImageSource.
// It handles decoding based on ContentType and potential re-encoding for WebP.
func processSingleImage(ctx context.Context, cfg *Config, source ImageSource) ProcessedImage {
	slog.Debug("Starting to process image source", "originalFilename", source.OriginalFilename, "index", source.Index, "contentType", source.ContentType)
	select {
	case <-ctx.Done():
		slog.Debug("Context cancelled before processing image source", "originalFilename", source.OriginalFilename)
		if source.Reader != nil {
			source.Reader.Close()
		}
		return ProcessedImage{Index: source.Index, OriginalFilename: source.OriginalFilename, Error: ctx.Err()}
	default:
	}

	if source.Reader == nil {
		slog.Warn("Image source reader is nil", "originalFilename", source.OriginalFilename)
		return ProcessedImage{Index: source.Index, OriginalFilename: source.OriginalFilename, Error: errors.New("image reader is nil")}
	}
	defer source.Reader.Close()

	processedInfo := ProcessedImage{Index: source.Index, OriginalFilename: source.OriginalFilename}
	var imgConfig image.Config
	var formatName string // Will store the detected format string from image.Decode/DecodeConfig
	var err error

	// Determine image type for gofpdf and processing path
	var imageTypeForPDF string
	var needsReEncoding bool

	switch source.ContentType {
	case "image/jpeg", "image/jpg":
		imageTypeForPDF = "JPG"
		needsReEncoding = false
	case "image/png":
		imageTypeForPDF = "PNG"
		needsReEncoding = false
	case "image/webp":
		imageTypeForPDF = "JPG" // WebP will be converted to JPG for PDF
		needsReEncoding = true
	default:
		// Try to decode config anyway, might be a known format with an unusual content type
		slog.Warn("Potentially unsupported content type, attempting to decode", "contentType", source.ContentType, "filename", source.OriginalFilename)
		// We need to "peek" at the format without consuming the reader for later full decode
		// This is tricky. For now, let's assume if ContentType is not one of above, we try generic decode.
		// A better way would be to use a TeeReader if we needed to DecodeConfig then Decode.
		// However, since we decode directly or re-encode, we can just proceed.
		img, detectedFormat, decodeErr := image.Decode(source.Reader)
		if decodeErr != nil {
			processedInfo.Error = fmt.Errorf("could not decode image (unknown content type %s) %s: %w", source.ContentType, source.OriginalFilename, decodeErr)
			return processedInfo
		}
		formatName = detectedFormat
		slog.Info("Decoded image with unknown initial content type", "detectedFormat", detectedFormat, "filename", source.OriginalFilename)

		// Reset reader if possible (not possible for http body without buffering, this is a simplification)
		// This part of the logic assumes source.Reader can be re-read or the 'img' is used directly.
		// For API, the reader is likely a one-shot deal.
		// If we decoded it, we must use the 'img' object.

		switch detectedFormat {
		case "jpeg":
			imageTypeForPDF = "JPG"
			needsReEncoding = false // It's already decoded, but we need to re-encode to pass to gofpdf if not JPG/PNG
			// To avoid re-encoding if not necessary, we'd need to pass the raw stream.
			// For simplicity now: if decoded, and it's JPEG, we'll re-encode to ensure it's in a buffer.
			// This is a slight inefficiency for JPEGs that fell into this path.
			buf := bufferPool.Get().(*bytes.Buffer)
			buf.Reset()
			if err := imaging.Encode(buf, img, imaging.JPEG, imaging.JPEGQuality(cfg.JPEGQuality)); err != nil {
				bufferPool.Put(buf)
				processedInfo.Error = fmt.Errorf("could not re-encode %s (originally %s) to jpg: %w", source.OriginalFilename, detectedFormat, err)
				return processedInfo
			}
			processedInfo.Reader = buf
			processedInfo.Width = float64(img.Bounds().Dx())
			processedInfo.Height = float64(img.Bounds().Dy())
			processedInfo.ImageTypeForPDF = "JPG"
			slog.Debug("Successfully processed image (decoded from unknown type)", "filename", source.OriginalFilename, "originalFormat", formatName, "pdfType", imageTypeForPDF, "width", processedInfo.Width, "height", processedInfo.Height)
			return processedInfo

		case "png":
			imageTypeForPDF = "PNG"
			needsReEncoding = false // Similar to JPEG, re-encode to buffer for consistent handling
			buf := bufferPool.Get().(*bytes.Buffer)
			buf.Reset()
			if err := imaging.Encode(buf, img, imaging.PNG); err != nil {
				bufferPool.Put(buf)
				processedInfo.Error = fmt.Errorf("could not re-encode %s (originally %s) to png: %w", source.OriginalFilename, detectedFormat, err)
				return processedInfo
			}
			processedInfo.Reader = buf
			processedInfo.Width = float64(img.Bounds().Dx())
			processedInfo.Height = float64(img.Bounds().Dy())
			processedInfo.ImageTypeForPDF = "PNG"
			slog.Debug("Successfully processed image (decoded from unknown type)", "filename", source.OriginalFilename, "originalFormat", formatName, "pdfType", imageTypeForPDF, "width", processedInfo.Width, "height", processedInfo.Height)
			return processedInfo
		case "webp":
			imageTypeForPDF = "JPG" // WebP will be converted to JPG for PDF
			needsReEncoding = true  // It's decoded, but needs re-encoding to JPG
		default:
			processedInfo.Error = fmt.Errorf("unsupported image format '%s' for %s (content type: %s)", detectedFormat, source.OriginalFilename, source.ContentType)
			return processedInfo
		}
		// If we are here, it means we decoded 'img' and it's webp, or jpeg/png that needs re-encoding to buffer.
		// Re-use the decoded 'img' for webp conversion or jpeg/png buffering.
		if needsReEncoding { // True for WebP, or if we decided to re-encode for jpeg/png in this path
			slog.Debug("Processing image that needs re-encoding", "filename", source.OriginalFilename, "originalFormat", formatName)
			if formatName == "webp" { // Explicitly handle 16-bit WebP
				switch img.(type) {
				case *image.Gray16, *image.NRGBA64, *image.RGBA64:
					slog.Debug("Converting 16-bit WebP image to 8-bit NRGBA", "filename", source.OriginalFilename)
					img = imaging.Clone(img) // imaging.Clone converts to NRGBA
				}
			}
			buf := bufferPool.Get().(*bytes.Buffer)
			buf.Reset()
			targetFormat := imaging.JPEG
			if imageTypeForPDF == "PNG" { // Should not happen if needsReEncoding is true for PNG from unknown type
				targetFormat = imaging.PNG
			}

			encodeOptions := []imaging.EncodeOption{}
			if targetFormat == imaging.JPEG {
				encodeOptions = append(encodeOptions, imaging.JPEGQuality(cfg.JPEGQuality))
			}

			if err := imaging.Encode(buf, img, targetFormat, encodeOptions...); err != nil {
				bufferPool.Put(buf)
				processedInfo.Error = fmt.Errorf("could not re-encode %s (format %s) to %s: %w", source.OriginalFilename, formatName, imageTypeForPDF, err)
				return processedInfo
			}
			processedInfo.Reader = buf
			processedInfo.Width = float64(img.Bounds().Dx())
			processedInfo.Height = float64(img.Bounds().Dy())
			processedInfo.ImageTypeForPDF = imageTypeForPDF
			slog.Debug("Successfully processed image (re-encoded)", "filename", source.OriginalFilename, "originalFormat", formatName, "pdfType", imageTypeForPDF, "width", processedInfo.Width, "height", processedInfo.Height)
			return processedInfo
		}
		// Fallthrough if not handled, though logic above should cover it.
		processedInfo.Error = fmt.Errorf("internal error processing image %s with detected format %s", source.OriginalFilename, formatName)
		return processedInfo
	}

	// Standard path for known content types (JPG, PNG, WebP)
	if !needsReEncoding { // JPG or PNG
		slog.Debug("Processing as PNG/JPG (direct reader)", "filename", source.OriginalFilename)
		// We need to pass the original reader to gofpdf for JPG/PNG.
		// However, we also need the dimensions. DecodeConfig first.
		// This means the reader might be consumed. We need a TeeReader or to buffer it.
		// For simplicity, let's read into a buffer first. This is less memory efficient for large files
		// but simplifies handling and ensures the reader can be used by gofpdf.

		data, readErr := io.ReadAll(source.Reader)
		if readErr != nil {
			processedInfo.Error = fmt.Errorf("could not read image data for %s: %w", source.OriginalFilename, readErr)
			return processedInfo
		}

		imgConfig, formatName, err = image.DecodeConfig(bytes.NewReader(data))
		if err != nil {
			processedInfo.Error = fmt.Errorf("could not decode image config for %s: %w", source.OriginalFilename, err)
			return processedInfo
		}

		processedInfo.Reader = bytes.NewReader(data) // Pass the buffered data
		processedInfo.Width = float64(imgConfig.Width)
		processedInfo.Height = float64(imgConfig.Height)
		processedInfo.ImageTypeForPDF = imageTypeForPDF
	} else { // WebP
		slog.Debug("Processing as WEBP (decode and re-encode to JPG)", "filename", source.OriginalFilename)
		decodedImg, webpFormatName, err := image.Decode(source.Reader)
		if err != nil {
			processedInfo.Error = fmt.Errorf("could not decode webp image %s: %w", source.OriginalFilename, err)
			return processedInfo
		}
		formatName = webpFormatName // Store the actual decoded format name

		// Handle 16-bit depth WebP by converting to 8-bit NRGBA before JPEG encoding
		switch decodedImg.(type) {
		case *image.Gray16, *image.NRGBA64, *image.RGBA64:
			slog.Debug("Converting 16-bit WebP image to 8-bit NRGBA", "filename", source.OriginalFilename)
			// imaging.Clone converts to NRGBA which is 8-bit per channel
			decodedImg = imaging.Clone(decodedImg)
		}

		buf := bufferPool.Get().(*bytes.Buffer)
		buf.Reset()
		if err := imaging.Encode(buf, decodedImg, imaging.JPEG, imaging.JPEGQuality(cfg.JPEGQuality)); err != nil {
			bufferPool.Put(buf)
			processedInfo.Error = fmt.Errorf("could not re-encode webp %s to jpg: %w", source.OriginalFilename, err)
			return processedInfo
		}
		processedInfo.Reader = buf
		processedInfo.Width = float64(decodedImg.Bounds().Dx())
		processedInfo.Height = float64(decodedImg.Bounds().Dy())
		processedInfo.ImageTypeForPDF = "JPG" // Always JPG for WebP
	}

	slog.Debug("Successfully processed image", "filename", source.OriginalFilename, "originalFormat", formatName, "pdfType", imageTypeForPDF, "width", processedInfo.Width, "height", processedInfo.Height)
	return processedInfo
}

// processImagesConcurrently processes a list of ImageSource concurrently.
func processImagesConcurrently(ctx context.Context, cfg *Config, imageSources []ImageSource) []ProcessedImage {
	slog.Debug("Starting concurrent image processing", "numSources", len(imageSources), "numWorkers", cfg.NumWorkers)
	if len(imageSources) == 0 {
		return []ProcessedImage{}
	}

	processedImageChan := make(chan ProcessedImage, len(imageSources)) // Buffered channel
	semaphoreChan := make(chan struct{}, cfg.NumWorkers)
	var wg sync.WaitGroup
	results := make([]ProcessedImage, len(imageSources))

	for i, source := range imageSources {
		select {
		case <-ctx.Done():
			slog.Info("Cancellation detected before starting all goroutines for image sources", "lastProcessedIndex", i-1, "filename", source.OriginalFilename)
			// Mark remaining as cancelled
			for j := i; j < len(imageSources); j++ {
				if results[j].OriginalFilename == "" { // Check if not already processed by a fast finishing goroutine
					results[j] = ProcessedImage{Index: imageSources[j].Index, OriginalFilename: imageSources[j].OriginalFilename, Error: ctx.Err()}
					if imageSources[j].Reader != nil {
						imageSources[j].Reader.Close() // Ensure readers are closed
					}
				}
			}
			goto endGoroutineLoop // Break out of the loop
		default:
		}

		wg.Add(1)
		go func(src ImageSource) {
			defer wg.Done()
			slog.Debug("Goroutine started for image source", "filename", src.OriginalFilename, "index", src.Index)
			select {
			case semaphoreChan <- struct{}{}:
				defer func() { <-semaphoreChan }()
			case <-ctx.Done():
				slog.Debug("Cancellation detected before acquiring semaphore for image source", "filename", src.OriginalFilename)
				if src.Reader != nil {
					src.Reader.Close()
				}
				processedImageChan <- ProcessedImage{Index: src.Index, OriginalFilename: src.OriginalFilename, Error: ctx.Err()}
				return
			}

			// Check context again before potentially long operation
			select {
			case <-ctx.Done():
				slog.Debug("Cancellation detected just before processing image source", "filename", src.OriginalFilename)
				if src.Reader != nil {
					src.Reader.Close()
				}
				processedImageChan <- ProcessedImage{Index: src.Index, OriginalFilename: src.OriginalFilename, Error: ctx.Err()}
				return
			default:
				processedResult := processSingleImage(ctx, cfg, src) // src.Reader is closed by processSingleImage
				select {
				case processedImageChan <- processedResult:
				case <-ctx.Done():
					slog.Debug("Cancellation detected while trying to send result for image source", "filename", src.OriginalFilename)
					// If result was successful but now cancelled, update error
					if processedResult.Error == nil {
						processedResult.Error = ctx.Err()
					}
					// Clean up reader if it wasn't closed due to early exit in processSingleImage
					if closer, ok := processedResult.Reader.(io.Closer); ok {
						closer.Close()
					} else if buf, ok := processedResult.Reader.(*bytes.Buffer); ok {
						bufferPool.Put(buf)
					}
					// Attempt to send anyway for accounting, or it might block wg.Wait if channel is full and main routine exited.
					// However, with buffered channel and proper draining, this might not be strictly necessary.
					// For safety, try non-blocking send or ensure channel is drained.
					// Since channel is buffered to len(imageSources), this send should not block.
					processedImageChan <- processedResult
				}
			}
		}(source)
	}

endGoroutineLoop:

	go func() {
		wg.Wait()
		close(processedImageChan)
		close(semaphoreChan) // Close semaphore channel once all workers are done
		slog.Debug("All image processing goroutines completed.")
	}()

	// Collect results
	// Initialize results with a placeholder to detect if a slot was filled
	for i := range results {
		results[i].Index = -1 // Mark as not filled
	}

	for res := range processedImageChan {
		if res.Index >= 0 && res.Index < len(results) {
			results[res.Index] = res
		} else {
			slog.Error("Received processed image with out-of-bounds index", "index", res.Index, "filename", res.OriginalFilename)
			// Clean up resources if any, though processSingleImage should handle its own.
			if res.Error == nil { // If no error but bad index, still clean up reader
				if closer, ok := res.Reader.(io.Closer); ok {
					closer.Close()
				} else if buf, ok := res.Reader.(*bytes.Buffer); ok {
					bufferPool.Put(buf)
				}
			}
		}
	}

	// Ensure all results slots are filled, especially if cancellation happened early
	if ctx.Err() != nil {
		for _, src := range imageSources {
			// Check if the result for this index was not set or was set but then processing was cancelled
			// If results[src.Index] is still the initial placeholder or has no error yet.
			// src.Index should be the correct one.
			if src.Index >= 0 && src.Index < len(results) && (results[src.Index].Index == -1 || results[src.Index].OriginalFilename == "") {
				results[src.Index] = ProcessedImage{Index: src.Index, OriginalFilename: src.OriginalFilename, Error: ctx.Err()}
			} else if src.Index >= 0 && src.Index < len(results) && results[src.Index].Error == nil {
				// If it was processed but context cancelled during collection, ensure error is set
				results[src.Index].Error = ctx.Err()
				// Clean up associated reader if it exists and is not already closed
				if closer, ok := results[src.Index].Reader.(io.Closer); ok {
					closer.Close()
				} else if buf, ok := results[src.Index].Reader.(*bytes.Buffer); ok {
					bufferPool.Put(buf)
				}
				results[src.Index].Reader = nil // Nullify reader as it's unusable
			}
		}
	}

	slog.Debug("Finished collecting image processing results.")
	return results
}

// generatePDFFromProcessedImages generates a PDF from a slice of ProcessedImage.
// The writer `w` is where the PDF output will be written.
func generatePDFFromProcessedImages(ctx context.Context, writer io.Writer, processedImages []ProcessedImage, pdf *gofpdf.Fpdf) (hasContent bool, err error) {
	slog.Debug("Starting PDF generation from processed images", "numImages", len(processedImages))
	hasContent = false

	// Sort processedImages by original index to ensure correct order in PDF
	sort.SliceStable(processedImages, func(i, j int) bool {
		return processedImages[i].Index < processedImages[j].Index
	})

	for i, res := range processedImages {
		select {
		case <-ctx.Done():
			slog.Info("Cancellation detected before adding image to PDF", "filename", res.OriginalFilename)
			// Clean up reader if processing was successful but cancelled here
			if res.Error == nil {
				if closer, ok := res.Reader.(io.Closer); ok {
					closer.Close()
				} else if buf, ok := res.Reader.(*bytes.Buffer); ok {
					bufferPool.Put(buf)
				}
			}
			return hasContent, ctx.Err()
		default:
		}

		if res.Error != nil {
			if errors.Is(res.Error, context.Canceled) {
				slog.Debug("Skipping image due to earlier cancellation", "filename", res.OriginalFilename)
			} else {
				slog.Warn("Skipping image due to error during its processing", "filename", res.OriginalFilename, "error", res.Error)
			}
			// Ensure any associated reader/buffer is cleaned up if an error occurred during processing
			if closer, ok := res.Reader.(io.Closer); ok {
				closer.Close()
			} else if buf, ok := res.Reader.(*bytes.Buffer); ok {
				bufferPool.Put(buf)
			}
			continue
		}
		if res.Reader == nil {
			slog.Warn("Reader for image is nil, skipping", "filename", res.OriginalFilename)
			continue
		}

		slog.Debug("Adding image to PDF", "filename", res.OriginalFilename, "width", res.Width, "height", res.Height, "type", res.ImageTypeForPDF)

		// Ensure the reader is handled correctly (closed or buffer returned to pool)
		readerToClean := res.Reader
		defer func(r io.Reader) {
			if fCloser, ok := r.(*os.File); ok { // Should not happen with API based sources
				fCloser.Close()
			} else if bReader, ok := r.(*bytes.Buffer); ok {
				bufferPool.Put(bReader)
			} else if rc, ok := r.(io.ReadCloser); ok { // Generic ReadCloser from ImageSource after processing
				rc.Close()
			}
		}(readerToClean)

		pdf.AddPageFormat("P", gofpdf.SizeType{Wd: res.Width, Ht: res.Height})
		if pdf.Err() {
			slog.Warn("Could not add page to PDF for image", "filename", res.OriginalFilename, "error", pdf.Error())
			pdf.ClearError()
			continue // Skip this image
		}

		imageName := fmt.Sprintf("image%d_%d", res.Index, i) // Ensure unique name
		// Use res.Reader directly. It's either a *bytes.Buffer (for webp/re-encoded) or a *bytes.Reader (for direct jpg/png)
		pdf.RegisterImageOptionsReader(imageName, gofpdf.ImageOptions{ImageType: res.ImageTypeForPDF, ReadDpi: false}, res.Reader)

		if pdf.Err() {
			slog.Warn("Could not register image in PDF", "filename", res.OriginalFilename, "error", pdf.Error())
			pdf.ClearError()
			continue // Skip this image
		}

		pdf.ImageOptions(imageName, 0, 0, res.Width, res.Height, false, gofpdf.ImageOptions{ImageType: res.ImageTypeForPDF}, 0, "")
		if pdf.Err() {
			slog.Warn("Could not place image on PDF page", "filename", res.OriginalFilename, "error", pdf.Error())
			pdf.ClearError()
			continue // Skip this image
		}
		hasContent = true
		slog.Debug("Successfully added image to PDF", "filename", res.OriginalFilename)
	}

	if pdf.Err() { // Check for any accumulated errors in gofpdf
		return hasContent, fmt.Errorf("error generating PDF structure: %w", pdf.Error())
	}

	select {
	case <-ctx.Done():
		slog.Info("Cancellation detected before writing PDF output.")
		return hasContent, ctx.Err()
	default:
	}

	if hasContent {
		slog.Debug("Writing PDF to output stream...")
		if err := pdf.Output(writer); err != nil {
			return true, fmt.Errorf("could not write PDF to writer: %w", err)
		}
		slog.Debug("Successfully wrote PDF to output stream.")
	} else {
		if ctx.Err() != nil { // If context was cancelled, and no content, return context error
			return false, ctx.Err()
		}
		// If no content but also no cancellation, it means all images failed or were skipped.
		if len(processedImages) > 0 {
			slog.Info("No content was added to the PDF (all images skipped or failed).")
		} else {
			slog.Info("No images processed and no content to add to PDF.")
		}
	}
	return hasContent, nil
}

// ConvertToPDF is the main entry point for the converter package.
// It takes a context, a list of ImageSource, a Config, and an io.Writer for the PDF output.
// It returns true if content was added to the PDF, and an error if one occurred.
func ConvertToPDF(ctx context.Context, sources []ImageSource, cfg *Config, writer io.Writer) (hasContent bool, err error) {
	slog.Debug("Starting PDF conversion process via converter package", "numSources", len(sources))
	select {
	case <-ctx.Done():
		return false, ctx.Err()
	default:
	}

	if len(sources) == 0 {
		slog.Info("No image sources provided for conversion.")
		return false, ErrNoSupportedImages
	}

	// Filter out sources that are obviously invalid before concurrent processing
	validSources := make([]ImageSource, 0, len(sources))
	for _, src := range sources {
		if src.Reader == nil && src.URL == "" {
			slog.Warn("Skipping image source with no reader and no URL", "originalFilename", src.OriginalFilename, "index", src.Index)
			// Potentially create a ProcessedImage with an error for this source if strict result parity is needed.
			// For now, just skip. The API handler will be responsible for creating valid ImageSource objects.
			continue
		}
		validSources = append(validSources, src)
	}

	if len(validSources) == 0 {
		slog.Info("No valid image sources after filtering.")
		// Close any readers from the original sources list if they were opened by the caller
		// (though the API handler should manage this lifecycle)
		for _, src := range sources {
			if src.Reader != nil {
				src.Reader.Close()
			}
		}
		return false, ErrNoSupportedImages
	}

	slog.Info("Processing valid image sources", "count", len(validSources))

	pdf := gofpdf.New("P", "pt", "A4", "") // Default page size, actual size set per image

	// Process images concurrently
	processedImageInfos := processImagesConcurrently(ctx, cfg, validSources)

	// Ensure all readers from original sources that might not have been consumed by
	// processImagesConcurrently (e.g. due to early cancellation) are closed.
	// processSingleImage is responsible for closing readers it processes.
	// Goroutines in processImagesConcurrently also attempt to close readers on cancellation.
	// This is a final safeguard.
	processedIndexes := make(map[int]bool)
	for _, pInfo := range processedImageInfos {
		processedIndexes[pInfo.Index] = true
	}
	for _, src := range validSources {
		if !processedIndexes[src.Index] && src.Reader != nil {
			// This source was intended for processing but didn't make it into processedImageInfos
			// or its goroutine exited very early.
			slog.Debug("Closing reader for unprocessed or early-cancelled source", "filename", src.OriginalFilename, "index", src.Index)
			src.Reader.Close()
		}
	}

	select {
	case <-ctx.Done():
		slog.Info("Cancellation detected before PDF generation phase in ConvertToPDF.")
		// Clean up any readers from successfully processed images that won't be used
		for _, info := range processedImageInfos {
			if info.Error == nil || !errors.Is(info.Error, context.Canceled) {
				if closer, ok := info.Reader.(io.Closer); ok {
					closer.Close()
				} else if buf, ok := info.Reader.(*bytes.Buffer); ok {
					bufferPool.Put(buf)
				}
			}
		}
		return false, ctx.Err()
	default:
	}

	// Generate PDF from processed images
	contentAdded, genErr := generatePDFFromProcessedImages(ctx, writer, processedImageInfos, pdf)
	if genErr != nil {
		if errors.Is(genErr, context.Canceled) {
			slog.Info("PDF generation was canceled.")
			return contentAdded, context.Canceled // Return contentAdded status along with cancellation
		}
		slog.Error("Failed during PDF generation", "error", genErr)
		return contentAdded, fmt.Errorf("pdf generation failed: %w", genErr)
	}

	if !contentAdded && len(validSources) > 0 {
		// Check if any processed image had an error OTHER than cancellation.
		// If all errors are cancellations, then the overall status is cancellation.
		// If there are other errors, it's more like "no content due to errors".
		allCancelled := true
		hasOtherErrors := false
		for _, pInfo := range processedImageInfos {
			if pInfo.Error != nil {
				if !errors.Is(pInfo.Error, context.Canceled) {
					allCancelled = false
					hasOtherErrors = true
					break
				}
			} else {
				// If an image was processed successfully but not added (e.g. PDF error for that specific image)
				// this also means not all were cancelled.
				allCancelled = false
			}
		}
		if ctx.Err() != nil { // Global context cancellation
			return false, ctx.Err()
		}
		if allCancelled && !hasOtherErrors && len(processedImageInfos) > 0 { // All were attempted but cancelled
			return false, context.Canceled // Or a more specific error if needed
		}
		// If no content and not due to cancellation of all items, return ErrNoSupportedImages
		return false, ErrNoSupportedImages
	}

	slog.Info("PDF conversion process completed", "contentAdded", contentAdded)
	return contentAdded, nil
}

// Helper function to determine content type from file extension
// This is a fallback if http.DetectContentType is not sufficient or not available (e.g. from filename only)
func GetContentTypeFromFilename(filename string) string {
	ext := strings.ToLower(filepath.Ext(filename))
	switch ext {
	case ".jpg", ".jpeg":
		return "image/jpeg"
	case ".png":
		return "image/png"
	case ".webp":
		return "image/webp"
	default:
		return "" // Unknown
	}
}

// FetchImage downloads an image from a URL.
// It returns an ImageSource with the Reader populated, or an error.
// The caller is responsible for closing the ImageSource.Reader.
func FetchImage(ctx context.Context, imageURL string, index int) (ImageSource, error) {
	slog.Debug("Fetching image from URL", "url", imageURL, "index", index)

	req, err := http.NewRequestWithContext(ctx, "GET", imageURL, nil)
	if err != nil {
		slog.Error("Failed to create request for URL", "url", imageURL, "error", err)
		return ImageSource{}, fmt.Errorf("failed to create request for %s: %w", imageURL, err)
	}

	client := &http.Client{} // Consider customizing timeout
	resp, err := client.Do(req)
	if err != nil {
		slog.Error("Failed to fetch image from URL", "url", imageURL, "error", err)
		return ImageSource{}, fmt.Errorf("failed to fetch %s: %w", imageURL, err)
	}
	// Caller must close resp.Body via ImageSource.Reader.Close()

	if resp.StatusCode != http.StatusOK {
		resp.Body.Close()
		slog.Warn("Failed to fetch image, non-OK status", "url", imageURL, "status", resp.StatusCode)
		return ImageSource{}, fmt.Errorf("failed to fetch %s: status %s", imageURL, resp.Status)
	}

	contentType := resp.Header.Get("Content-Type")
	// Basic validation of content type
	if !strings.HasPrefix(strings.ToLower(contentType), "image/") {
		resp.Body.Close()
		slog.Warn("Unsupported content type from URL", "url", imageURL, "contentType", contentType)
		return ImageSource{}, fmt.Errorf("%w: %s from %s", ErrUnsupportedContentType, contentType, imageURL)
	}

	// Try to get a filename from URL
	filename := filepath.Base(imageURL)
	parsedURL, parseErr := url.ParseRequestURI(imageURL)
	if parseErr == nil {
		filename = filepath.Base(parsedURL.Path)
	}

	return ImageSource{
		OriginalFilename: filename,
		Reader:           resp.Body, // This is an io.ReadCloser
		URL:              imageURL,
		ContentType:      contentType,
		Index:            index,
	}, nil
}
