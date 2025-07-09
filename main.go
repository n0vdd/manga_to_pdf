package main

import (
	"bytes"
	"errors"
	"flag"
	"fmt"
	"github.com/disintegration/imaging" // Added for image conversion
	"github.com/jung-kurt/gofpdf"
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

// generatePDFFromDecodedImages creates a PDF from a slice of processed images.
func generatePDFFromDecodedImages(writer io.Writer, processedImages []ProcessedImage) (hasContent bool, err error) {
	hasContent = false
	// Initialize PDF: "P" for portrait, "pt" for points, "A4" is default but we override
	pdf := gofpdf.New("P", "pt", "A4", "")
	// pdf.SetErrorTolerance(gofpdf.ContinueOnError) // Or handle errors manually

	for i, res := range processedImages {
		if res.Error != nil {
			log.Printf("... ⚠️ Error processing %s: %v. Skipping.", res.Filename, res.Error)
			continue
		}
		if res.Image == nil {
			log.Printf("... ⚠️ Image data for %s is nil. Skipping.", res.Filename)
			continue
		}

		decodedImg := res.Image
		widthPt := float64(decodedImg.Bounds().Dx())  // Assuming 1 pixel = 1 point for direct mapping
		heightPt := float64(decodedImg.Bounds().Dy()) // Consider DPI if images have it

		// Add page with specific dimensions of the image
		// The size unit is "pt" as set in New()
		pdf.AddPageFormat("P", gofpdf.SizeType{Wd: widthPt, Ht: heightPt})
		if pdf.Err() {
			log.Printf("... ⚠️ Could not add page for image %s to PDF: %v. Skipping.", res.Filename, pdf.Error())
			pdf.ClearError() // Clear error to attempt next image
			continue
		}

		// Register the image. gofpdf needs a name for the image.
		// We can use the filename or an index. Using index to ensure uniqueness.
		imageName := fmt.Sprintf("image%d", i)

		// We need to encode the image.Image to a supported format (PNG, JPG) in memory
		// as gofpdf's RegisterImageOptionsReader takes an io.Reader.
		// PNG is lossless and generally good quality.
		var imgBuf bytes.Buffer
		// Convert to NRGBA if not already, as png.Encode expects it or similar common types.
		// Our decodeSingleImage already ensures finalImg is NRGBA.
		if encodeErr := imaging.Encode(&imgBuf, decodedImg, imaging.PNG); encodeErr != nil {
			log.Printf("... ⚠️ Could not encode image %s to PNG: %v. Skipping.", res.Filename, encodeErr)
			continue
		}

		// Register image from the buffer.
		// The image type can be "PNG", "JPG", or "GIF".
		// "" lets gofpdf determine from stream, but specifying is safer.
		pdf.RegisterImageOptionsReader(imageName, gofpdf.ImageOptions{ImageType: "PNG", ReadDpi: false}, &imgBuf)
		if pdf.Err() {
			log.Printf("... ⚠️ Could not register image %s in PDF: %v. Skipping.", res.Filename, pdf.Error())
			pdf.ClearError()
			continue
		}

		// Place the image on the page.
		// Use width and height of the page (which is sized to the image).
		// x, y = 0, 0 for top-left corner.
		// flow = false means absolute positioning.
		pdf.ImageOptions(imageName, 0, 0, widthPt, heightPt, false, gofpdf.ImageOptions{ImageType: "PNG"}, 0, "")
		if pdf.Err() {
			log.Printf("... ⚠️ Could not place image %s on PDF page: %v. Skipping.", res.Filename, pdf.Error())
			pdf.ClearError()
			continue
		}
		hasContent = true
	}

	if !hasContent && len(processedImages) > 0 {
		log.Println("ℹ️ No images were successfully added to the PDF pages.")
	}

	if pdf.Err() { // Check for any accumulated errors before writing
		return hasContent, fmt.Errorf("error generating PDF structure: %w", pdf.Error())
	}

	if hasContent {
		if err := pdf.Output(writer); err != nil {
			return true, fmt.Errorf("could not write PDF to writer: %w", err)
		}
	} else if len(processedImages) > 0 {
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
