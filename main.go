package main

import (
	"errors"
	"flag"
	"fmt"
	"github.com/disintegration/imaging" // Added for image conversion
	"github.com/signintech/gopdf"
	_ "golang.org/x/image/webp" // Added for WebP decoding (register decoder)
	"image"                     // Added for image manipulation
	"image/draw"                // Added for explicit conversion to NRGBA
	_ "image/jpeg"              // Added for JPEG decoding (register decoder)
	_ "image/png"               // Added for PNG encoding (register decoder)
	"io"
	"log"
	"os"
	"path/filepath"
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
	flag.Parse()

	outFile, err := os.Create(*outputFile)
	if err != nil {
		log.Fatalf("❌ Could not create output file: %v", err)
	}
	// defer outFile.Close() // We will handle closing manually or more conditionally

	hasContent, err := convertImagesToPDF(*inputDir, outFile)

	// Ensure file is closed regardless of outcome, before potential removal
	if closeErr := outFile.Close(); closeErr != nil {
		log.Printf("⚠️ Warning: failed to close output file %s: %v", *outputFile, closeErr)
		// If there was already an error, prioritize reporting that one.
		// If not, this close error might be the primary issue.
		if err == nil {
			err = fmt.Errorf("failed to close output file: %w", closeErr)
		}
	}

	if err != nil {
		// If no content was generated or the specific error is ErrNoSupportedFiles, remove the created file.
		if !hasContent || errors.Is(err, ErrNoSupportedFiles) {
			if removeErr := os.Remove(*outputFile); removeErr != nil {
				log.Printf("⚠️ Warning: failed to remove empty output file %s: %v", *outputFile, removeErr)
			}
		}
		log.Fatalf("❌ Failed to convert images to PDF: %v", err)
	}

	if !hasContent {
		// This case should ideally be caught by an error from convertImagesToPDF if no images were processed.
		// However, as a safeguard:
		log.Printf("ℹ️ No images were successfully added to the PDF from '%s'. Output file '%s' removed.", *inputDir, *outputFile)
		if removeErr := os.Remove(*outputFile); removeErr != nil {
			log.Printf("⚠️ Warning: failed to remove output file %s after no content: %v", *outputFile, removeErr)
		}
		return // Graceful exit
	}

	fmt.Printf("✅ Successfully created '%s' from images in '%s'\n", *outputFile, *inputDir)
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

// generatePDFFromDecodedImages creates a PDF from a slice of processed images.
func generatePDFFromDecodedImages(writer io.Writer, processedImages []ProcessedImage) (hasContent bool, err error) {
	hasContent = false
	pdf := gopdf.GoPdf{}
	pdf.Start(gopdf.Config{PageSize: *gopdf.PageSizeA4}) // Default, pages will resize

	for _, res := range processedImages {
		if res.Error != nil {
			log.Printf("... ⚠️ Error processing %s: %v. Skipping.", res.Filename, res.Error)
			continue
		}
		if res.Image == nil { // Should not happen if Error is nil, but good check
			log.Printf("... ⚠️ Image data for %s is nil. Skipping.", res.Filename)
			continue
		}

		// fmt.Printf("Adding to PDF: %s\n", res.Filename) // Verbose, consider removing or conditional logging
		decodedImg := res.Image
		width := float64(decodedImg.Bounds().Dx())
		height := float64(decodedImg.Bounds().Dy())

		pageOptions := gopdf.PageOption{
			PageSize: &gopdf.Rect{W: width, H: height},
		}
		pdf.AddPageWithOption(pageOptions)

		if imgErr := pdf.ImageFrom(decodedImg, 0, 0, &gopdf.Rect{W: width, H: height}); imgErr != nil {
			log.Printf("... ⚠️ Could not add image %s to PDF: %v. Skipping.", res.Filename, imgErr)
			continue // Skip this image, try to add others
		}
		hasContent = true // At least one image was successfully added
	}

	if !hasContent && len(processedImages) > 0 {
		// All images resulted in errors or were skipped, but there were images to process.
		// Return an error if appropriate, or just false for hasContent.
		// For now, we rely on hasContent=false being returned.
		// If all images fail, but there are no actual *PDF writing* errors, err will be nil.
		log.Println("ℹ️ No images were successfully added to the PDF pages.")
	}


	if !hasContent && len(processedImages) == 0 {
		// This case means findSupportedImageFiles found files, but decodeImagesConcurrently returned an empty slice,
		// which it shouldn't if imageFiles was not empty. Or generatePDFFromDecodedImages was called with an empty slice.
		// This should be caught earlier by findSupportedImageFiles returning ErrNoSupportedFiles.
		// If we reach here with no content and no files attempted, it's unusual.
		// However, the primary check for empty inputDir is in findSupportedImageFiles.
		// The main function will also handle hasContent=false if convertImagesToPDF returns it.
	}


	// Only write to PDF if there's actual content to prevent empty PDFs from gopdf if it would create one.
	// However, gopdf.WriteTo is likely fine with no pages, but our hasContent flag is more explicit.
	if hasContent {
		if _, writeErr := pdf.WriteTo(writer); writeErr != nil {
			return true, fmt.Errorf("could not write PDF to writer: %w", writeErr) // true because content was there before write error
		}
	} else if len(processedImages) > 0 {
		// No content, but images were attempted. This implies all images failed to be added.
		// We don't return an error here unless pdf.WriteTo itself fails,
		// but main will check hasContent.
		log.Println("No content was added to the PDF, so no PDF file will be written.")
	}


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
