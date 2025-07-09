package main

import (
	"errors"
	"flag"
	"fmt"
	"github.com/disintegration/imaging" // Added for image conversion
	"github.com/pdfcpu/pdfcpu/pkg/api"
	"github.com/pdfcpu/pdfcpu/pkg/pdfcpu/model"
	_ "golang.org/x/image/webp" // Added for WebP decoding (register decoder)
	"image"                     // Added for image manipulation
	"image/draw"                // Added for explicit conversion to NRGBA
	_ "image/jpeg"              // Added for JPEG decoding (register decoder)
	_ "image/png"               // Added for PNG encoding (register decoder)
	"io"
	"log"
	"os"
	"path/filepath"
	"runtime" // Added for memory profiling
	"runtime/pprof" // Added for CPU and memory profiling
	"sort"
	"strings"
)

// ErrNoSupportedFiles is returned when no supported image files are found in a directory.
var ErrNoSupportedFiles = errors.New("no supported image files found")

// ProcessedImage holds the data for an image that has been processed (or attempted to be processed).
type ProcessedImage struct {
	Index    int         // Original index of the file, for ordering
	Filename string      // Original filename
	Image    image.Image // Decoded and processed image, nil if error
	Error    error       // Error encountered during processing, nil if successful
}

func main() {
	inputDir := flag.String("i", ".", "Input directory containing image files (.webp, .jpg, .jpeg, .png)")
	outputFile := flag.String("o", "output.pdf", "Output PDF file name")
	cpuprofile := flag.String("cpuprofile", "", "Write cpu profile to `file`")
	memprofile := flag.String("memprofile", "", "Write memory profile to `file`")
	flag.Parse()

	// CPU Profiling
	if *cpuprofile != "" {
		f, err := os.Create(*cpuprofile)
		if err != nil {
			log.Fatalf("could not create CPU profile: %v", err)
		}
		defer f.Close() // Ensure file is closed
		if err := pprof.StartCPUProfile(f); err != nil {
			log.Fatalf("could not start CPU profile: %v", err)
		}
		defer pprof.StopCPUProfile()
		log.Printf("ℹ️ CPU profiling enabled, output to %s", *cpuprofile)
	}

	// Main application logic
	runApp(*inputDir, *outputFile)

	// Memory Profiling
	if *memprofile != "" {
		f, err := os.Create(*memprofile)
		if err != nil {
			log.Fatalf("could not create memory profile: %v", err)
		}
		defer f.Close() // Ensure file is closed
		runtime.GC()    // Get up-to-date statistics
		if err := pprof.WriteHeapProfile(f); err != nil {
			log.Fatalf("could not write memory profile: %v", err)
		}
		log.Printf("ℹ️ Memory profile written to %s", *memprofile)
	}
}

// runApp encapsulates the core application logic.
func runApp(inputDir string, outputFile string) {
	outFile, err := os.Create(outputFile)
	if err != nil {
		log.Fatalf("❌ Could not create output file: %v", err)
	}
	// defer outFile.Close() is handled carefully below

	hasContent, err := convertImagesToPDF(inputDir, outFile)

	// Ensure file is closed regardless of outcome, before potential removal
	if closeErr := outFile.Close(); closeErr != nil {
		log.Printf("⚠️ Warning: failed to close output file %s: %v", outputFile, closeErr)
		if err == nil {
			err = fmt.Errorf("failed to close output file: %w", closeErr)
		}
	}

	if err != nil {
		if !hasContent || errors.Is(err, ErrNoSupportedFiles) {
			if removeErr := os.Remove(outputFile); removeErr != nil {
				log.Printf("⚠️ Warning: failed to remove empty output file %s: %v", outputFile, removeErr)
			}
		}
		log.Fatalf("❌ Failed to convert images to PDF: %v", err)
	}

	if !hasContent {
		log.Printf("ℹ️ No images were successfully added to the PDF from '%s'. Output file '%s' removed.", inputDir, outputFile)
		if removeErr := os.Remove(outputFile); removeErr != nil {
			log.Printf("⚠️ Warning: failed to remove output file %s after no content: %v", outputFile, removeErr)
		}
		return // Graceful exit
	}

	fmt.Printf("✅ Successfully created '%s' from images in '%s'\n", outputFile, inputDir)
}

// findSupportedImageFiles scans a directory for supported image types and returns a sorted list of filenames.
func findSupportedImageFiles(inputDir string) ([]string, error) {
	files, err := os.ReadDir(inputDir)
	if err != nil {
		return nil, fmt.Errorf("could not read directory %s: %w", inputDir, err)
	}

	var imageFiles []string
	supportedExtensions := map[string]bool{
		".webp": true, ".jpg": true, ".jpeg": true, ".png": true,
	}
	for _, file := range files {
		if !file.IsDir() && supportedExtensions[strings.ToLower(filepath.Ext(file.Name()))] {
			imageFiles = append(imageFiles, file.Name())
		}
	}

	if len(imageFiles) == 0 {
		// Wrap ErrNoSupportedFiles with more context.
		return nil, fmt.Errorf("%w in directory %s", ErrNoSupportedFiles, inputDir)
	}

	sort.Strings(imageFiles)
	return imageFiles, nil
}

// decodeSingleImage handles opening, decoding, and standardizing a single image.
func decodeSingleImage(inputDir string, filename string, idx int) ProcessedImage {
	fullPath := filepath.Join(inputDir, filename)
	// fmt.Printf("Decoding: %s\n", filename) // Moved to main processing loop or remove for less verbosity

	file, err := os.Open(fullPath)
	if err != nil {
		return ProcessedImage{Index: idx, Filename: filename, Error: fmt.Errorf("could not open file: %w", err)}
	}
	defer file.Close()

	decodedImg, formatName, err := image.Decode(file)
	if err != nil {
		return ProcessedImage{Index: idx, Filename: filename, Error: fmt.Errorf("could not decode image: %w", err)}
	}

	// Convert 16-bit depth images to 8-bit NRGBA via imaging.Clone
	// imaging.Clone converts to *image.NRGBA if the source is one of these types.
	switch decodedImg.(type) {
	case *image.Gray16, *image.NRGBA64, *image.RGBA64:
		log.Printf("... Converting 16-bit image %s (format: %s) to 8-bit NRGBA", filename, formatName)
		decodedImg = imaging.Clone(decodedImg)
	}

	// Ensure the image is image.NRGBA (8-bit, compatible with gopdf)
	// If imaging.Clone already converted it, or if it was already NRGBA, this might be redundant
	// but draw.Draw is safe.
	bounds := decodedImg.Bounds()
	finalImg := image.NewNRGBA(bounds)
	draw.Draw(finalImg, bounds, decodedImg, bounds.Min, draw.Src)

	return ProcessedImage{Index: idx, Filename: filename, Image: finalImg}
}

// decodeImagesConcurrently processes a list of image files concurrently.
func decodeImagesConcurrently(inputDir string, imageFiles []string, maxConcurrentDecoders int) []ProcessedImage {
	if len(imageFiles) == 0 {
		return []ProcessedImage{}
	}

	processedImageChan := make(chan ProcessedImage)
	semaphoreChan := make(chan struct{}, maxConcurrentDecoders)

	for i, filename := range imageFiles {
		go func(idx int, fname string) {
			semaphoreChan <- struct{}{}        // Acquire slot
			defer func() { <-semaphoreChan }() // Release slot

			log.Printf("Processing image: %s", fname)
			processedImageChan <- decodeSingleImage(inputDir, fname, idx)
		}(i, filename)
	}

	results := make([]ProcessedImage, len(imageFiles))
	for i := 0; i < len(imageFiles); i++ {
		res := <-processedImageChan
		results[res.Index] = res // Store in original order
	}
	close(processedImageChan)
	close(semaphoreChan) // Close semaphore channel once all goroutines are guaranteed to be done.

	return results
}

// generatePDFFromDecodedImages creates a PDF from a slice of processed images using pdfcpu.
func generatePDFFromDecodedImages(writer io.Writer, processedImages []ProcessedImage) (hasContent bool, err error) {
	hasContent = false
	var imageFilePaths []string
	var tempFiles []string // Keep track of temporary image files to delete later

	// Create a temporary directory for images
	tempImageDir, err := os.MkdirTemp("", "pdfcpu-images-")
	if err != nil {
		return false, fmt.Errorf("could not create temp directory for images: %w", err)
	}
	defer os.RemoveAll(tempImageDir) // Clean up temp image directory

	for _, res := range processedImages {
		if res.Error != nil {
			log.Printf("... ⚠️ Error processing %s: %v. Skipping.", res.Filename, res.Error)
			continue
		}
		if res.Image == nil {
			log.Printf("... ⚠️ Image data for %s is nil. Skipping.", res.Filename)
			continue
		}

		// Save image.Image to a temporary file
		// Ensure the temporary filename has a supported extension for pdfcpu
		ext := strings.ToLower(filepath.Ext(res.Filename))
		if ext == "" || (ext != ".png" && ext != ".jpg" && ext != ".jpeg" && ext != ".webp" && ext != ".tif" && ext != ".tiff") {
			// Default to .png if original extension is unknown or unsupported by direct saving
			// or if we need to re-encode (e.g. from a generic image.Image)
			ext = ".png" // pdfcpu supports png, jpg, webp, tiff
		}
		// Use a more robust temp file creation within the dedicated directory
		tempImageFile, err := os.CreateTemp(tempImageDir, fmt.Sprintf("image-*%s", ext))

		if err != nil {
			log.Printf("... ⚠️ Could not create temp file for image %s: %v. Skipping.", res.Filename, err)
			continue
		}

		// Encode the image to the temporary file
		// imaging.Save handles various formats based on extension.
		// We need to ensure the format is one pdfcpu explicitly supports (jpg, png, webp, tiff).
		// For simplicity, we can standardize on PNG for broadest compatibility from image.Image.
		// Or, attempt to save in original format if it's supported.
		// The current `decodeSingleImage` converts to NRGBA. PNG is a good lossless format for NRGBA.
		// Let's use imaging.Encode which writes to an io.Writer.

		// We need to decide the format. PNG is a safe bet.
		// If we want to preserve original format (if supported), more logic is needed.
		// For now, let's try to save as PNG.
		err = imaging.Encode(tempImageFile, res.Image, imaging.PNG)
		if err != nil {
			tempImageFile.Close() // Close before attempting to remove or logging path
			log.Printf("... ⚠️ Could not encode image %s to temp file %s: %v. Skipping.", res.Filename, tempImageFile.Name(), err)
			os.Remove(tempImageFile.Name()) // Clean up failed temp file
			continue
		}
		tempImageFilePath := tempImageFile.Name()
		tempImageFile.Close() // Close the file after successful write

		imageFilePaths = append(imageFilePaths, tempImageFilePath)
		tempFiles = append(tempFiles, tempImageFilePath) // Add to list for deferred cleanup
		hasContent = true                               // Mark that we have at least one image to process
	}

	var imageReaders []io.Reader
	var openedFiles []*os.File // Keep track of opened files to close them

	// This defer func will clean up temporary image files
	defer func() {
		for _, fPath := range tempFiles {
			if err := os.Remove(fPath); err != nil {
				log.Printf("⚠️ Warning: failed to remove temp image file %s: %v", fPath, err)
			}
		}
	}()
	// This defer func will close all opened image files
	defer func() {
		for _, f := range openedFiles {
			if f != nil {
				if err := f.Close(); err != nil {
					log.Printf("⚠️ Warning: failed to close temp image file %s: %v", f.Name(), err)
				}
			}
		}
	}()

	if !hasContent || len(imageFilePaths) == 0 {
		if len(processedImages) > 0 {
			log.Println("ℹ️ No images were successfully prepared for PDF generation.")
		}
		return false, nil // No content to add to PDF
	}

	// Open each temporary image file for reading
	for _, fPath := range imageFilePaths {
		f, err := os.Open(fPath)
		if err != nil {
			// This should ideally not happen if files were created successfully, but good to check
			log.Printf("... ⚠️ Could not open temp image file %s for reading: %v. Skipping.", fPath, err)
			// Consider if we should abort or try to continue with successfully opened files
			// For now, let's try to continue, but this image will be skipped.
			// To prevent ImportImages from processing a partial list if some files fail to open,
			// we might need to return an error here or ensure all readers are valid.
			// For simplicity, if one fails to open, we'll return an error for the whole batch.
			return true, fmt.Errorf("failed to open temporary image file %s for reading: %w", fPath, err)
		}
		imageReaders = append(imageReaders, f)
		openedFiles = append(openedFiles, f)
	}

	if len(imageReaders) == 0 && hasContent {
		// This means all images that were saved failed to be opened.
		log.Println("ℹ️ All prepared images failed to be opened for PDF generation.")
		return true, errors.New("all prepared images failed to be opened for PDF generation")
	}


	// Configuration for pdfcpu
	// For default behavior (image size dictates page size), pass nil for ImportConfig.
	// Pass a default model.Configuration.
	conf := model.NewDefaultConfiguration()
	// For default behavior (image size dictates page size), pass nil for the ImportConfig.
	// The api.ImportImages function expects *pdfcpu.Import for its `imp` parameter.
	// Examples from pdfcpu (like ImportImagesFile) show that passing `nil` for this
	// parameter yields default import behavior, which is what we want.

	log.Printf("ℹ️ Importing %d images into PDF via io.Writer", len(imageReaders))

	// api.ImportImages takes (rs io.ReadSeeker, w io.Writer, imgs []io.Reader, imp *pdfcpu.Import, conf *model.Configuration)
	// Pass `nil` for `imp` for default import settings.
	if err := api.ImportImages(nil, writer, imageReaders, nil, conf); err != nil {
		// hasContent is true because we attempted to process images.
		return true, fmt.Errorf("pdfcpu could not import images: %w", err)
	}

	// If successful, ImportImages has written directly to the writer.
	// hasContent was set if any image was successfully saved to a temp file.
	return hasContent, nil
}

// convertImagesToPDF coordinates finding, decoding, and generating a PDF from images.
// It returns true if content was successfully written to the PDF.
func convertImagesToPDF(inputDir string, writer io.Writer) (hasContent bool, err error) {
	imageFiles, err := findSupportedImageFiles(inputDir)
	if err != nil {
		return false, err // This will include the wrapped ErrNoSupportedFiles
	}

	log.Printf("Found %d image files to convert in %s.", len(imageFiles), inputDir)

	// For now, keep maxConcurrentDecoders as a constant. Could be configurable.
	const maxConcurrentDecoders = 4
	processedResultImages := decodeImagesConcurrently(inputDir, imageFiles, maxConcurrentDecoders)

	// Filter out images that had processing errors before sending to PDF generation
	// This might be redundant if generatePDF handles errors, but can be cleaner.
	// For now, generatePDFFromDecodedImages handles errors internally by skipping.

	return generatePDFFromDecodedImages(writer, processedResultImages)
}

// processImage (old function, can be removed or kept if there's a use case for single, non-concurrent processing)
// For now, its logic has been integrated into decodeSingleImage.
/*
func processImage(imagePath string) (image.Image, error) {
	file, err := os.Open(imagePath)
	if err != nil {
		return nil, fmt.Errorf("could not open file: %w", err)
	}
	defer func(file *os.File) {
		err = file.Close()
		if err != nil {
			log.Fatalf("could not close image file %s: %v", imagePath, err)
		}
	}(file)

	decodedImg, formatName, err := image.Decode(file)
	if err != nil {
		return nil, fmt.Errorf("could not decode image: %w", err)
	}

	switch img := decodedImg.(type) {
	case *image.Gray16, *image.NRGBA64, *image.RGBA64:
		log.Printf("... Converting 16-bit image %s (format: %s) to 8-bit NRGBA via imaging.Clone", filepath.Base(imagePath), formatName)
		decodedImg = imaging.Clone(img)
	}

	bounds := decodedImg.Bounds()
	finalImg := image.NewNRGBA(bounds)
	draw.Draw(finalImg, bounds, decodedImg, bounds.Min, draw.Src)

	return finalImg, nil
}
*/
