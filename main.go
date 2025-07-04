package main

import (
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

func main() {
	// Define command-line flags for input and output paths - REMOVED
	inputDir := flag.String("i", ".", "Input directory containing image files (.webp, .jpg, .jpeg, .png)")
	outputFile := flag.String("o", "output.pdf", "Output PDF file name")
	flag.Parse()

	// Call the main conversion function - REMOVED
	outFile, err := os.Create(*outputFile)
	if err != nil {
		log.Fatalf("❌ Could not create output file: %v", err)
	}
	defer func(outFile *os.File) {
		err = outFile.Close()
		if err != nil {
			log.Fatalf("❌ Could not close output file %s: %v", *outputFile, err)
		}
	}(outFile)
	err = convertImagesToPDF(*inputDir, outFile)
	if err != nil {
		log.Fatalf("❌ Failed to convert images to PDF: %v", err)
	}
	fmt.Printf("✅ Successfully created '%s' from images in '%s'\n", *outputFile, *inputDir)
}

// convertImagesToPDF finds all supported image files, decodes them, and adds them to a PDF.
// It now writes the PDF directly to an io.Writer.
func convertImagesToPDF(inputDir string, writer io.Writer) error {
	// 1. Read all files from the input directory
	files, err := os.ReadDir(inputDir)
	if err != nil {
		return fmt.Errorf("could not read directory %s: %w", inputDir, err)
	}

	// 2. Filter for supported image files and store their names
	var imageFiles []string
	supportedExtensions := map[string]bool{
		".webp": true,
		".jpg":  true,
		".jpeg": true,
		".png":  true,
	}
	for _, file := range files {
		if !file.IsDir() && supportedExtensions[strings.ToLower(filepath.Ext(file.Name()))] {
			imageFiles = append(imageFiles, file.Name())
		}
	}

	if len(imageFiles) == 0 {
		return fmt.Errorf("no supported image files (.webp, .jpg, .jpeg, .png) found in directory %s", inputDir)
	}

	// 3. Sort the files alphabetically
	sort.Strings(imageFiles)
	log.Printf("Found %d image files to convert in %s.\n", len(imageFiles), inputDir)

	// 4. Initialize a new PDF document using gopdf
	pdf := gopdf.GoPdf{}
	pdf.Start(gopdf.Config{PageSize: *gopdf.PageSizeA4}) // Default, will be overridden

	// 5. Process images concurrently and add them to the PDF sequentially.

	// Define a struct to hold processed image data and its original index for ordering.
	type ProcessedImage struct {
		Index    int
		Filename string
		Image    image.Image
		Error    error
	}

	// Number of concurrent decoders. Let's use a reasonable number, e.g., number of CPUs or a fixed value.
	// For simplicity, let's use a fixed number like 4. This can be tuned.
	// runtime.NumCPU() could also be used.
	maxConcurrentDecoders := 4

	// Create a channel to send processed image data.
	// The buffer size is len(imageFiles) to prevent blocking if PDF processing is slow,
	// though with a semaphore, this might not be strictly necessary if workers block on semaphore.
	// Let's make it unbuffered and rely on the semaphore for backpressure.
	processedImageChan := make(chan ProcessedImage)
	// Create a semaphore channel to limit concurrency.
	semaphoreChan := make(chan struct{}, maxConcurrentDecoders)

	// Launch goroutines to decode images.
	for i, filename := range imageFiles {
		go func(idx int, fname string) {
			semaphoreChan <- struct{}{}        // Acquire a slot
			defer func() { <-semaphoreChan }() // Release the slot

			fullPath := filepath.Join(inputDir, fname)
			fmt.Printf("Decoding: %s\n", fname)

			file, err := os.Open(fullPath)
			if err != nil {
				processedImageChan <- ProcessedImage{Index: idx, Filename: fname, Error: fmt.Errorf("could not open file: %w", err)}
				return
			}
			defer file.Close()

			// Use image.Decode which automatically detects the format.
			// Ensure necessary image format packages (image/jpeg, image/png, golang.org/x/image/webp) are imported for decoder registration.
			decodedImg, formatName, err := image.Decode(file) // The second return value is format string, ignored here.
			if err != nil {
				processedImageChan <- ProcessedImage{Index: idx, Filename: fname, Error: fmt.Errorf("could not decode image: %w", err)}
				return
			}

			// Check for 16-bit depth images and convert them to 8-bit NRGBA
			switch img := decodedImg.(type) {
			case *image.Gray16, *image.NRGBA64, *image.RGBA64:
				log.Printf("... Converting 16-bit image %s (format: %s) to 8-bit NRGBA via imaging.Clone", fname, formatName)
				decodedImg = imaging.Clone(img) // imaging.Clone should return an *image.NRGBA
			}

			// Explicitly convert to image.NRGBA to ensure 8-bit depth and compatible color model for gopdf
			bounds := decodedImg.Bounds()
			finalImg := image.NewNRGBA(bounds)
			draw.Draw(finalImg, bounds, decodedImg, bounds.Min, draw.Src)

			processedImageChan <- ProcessedImage{Index: idx, Filename: fname, Image: finalImg}
		}(i, filename)
	}

	// Collect results and add to PDF.
	// We need to store them temporarily to ensure correct order if they arrive out of order.
	results := make([]ProcessedImage, len(imageFiles))
	for i := 0; i < len(imageFiles); i++ {
		res := <-processedImageChan
		results[res.Index] = res // Store in the correct order using the original index
	}
	close(processedImageChan)
	close(semaphoreChan)

	// Now add images to PDF in the original sorted order.
	for _, res := range results {
		if res.Error != nil {
			log.Printf("... ⚠️  Error processing %s: %v. Skipping.", res.Filename, res.Error)
			continue
		}

		fmt.Printf("Adding to PDF: %s\n", res.Filename)
		decodedImg := res.Image
		width := float64(decodedImg.Bounds().Dx())
		height := float64(decodedImg.Bounds().Dy())

		pageOptions := gopdf.PageOption{
			PageSize: &gopdf.Rect{W: width, H: height},
		}
		pdf.AddPageWithOption(pageOptions)

		err := pdf.ImageFrom(decodedImg, 0, 0, &gopdf.Rect{W: width, H: height})
		if err != nil {
			log.Printf("... ⚠️  could not draw image %s on PDF using ImageFrom: %v. Skipping.", res.Filename, err)
			continue
		}
	}

	// 6. Write the final PDF directly to the provided io.Writer.
	_, err = pdf.WriteTo(writer)
	if err != nil {
		return fmt.Errorf("could not write PDF to writer: %w", err)
	}

	return nil
}

// processImage handles opening, decoding, and converting a single image.
// It returns the processed image.Image or an error.
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

	// Use image.Decode which automatically detects the format.
	decodedImg, formatName, err := image.Decode(file)
	if err != nil {
		return nil, fmt.Errorf("could not decode image: %w", err)
	}

	// Check for 16-bit depth images and convert them to 8-bit NRGBA
	switch img := decodedImg.(type) {
	case *image.Gray16, *image.NRGBA64, *image.RGBA64:
		log.Printf("... Converting 16-bit image %s (format: %s) to 8-bit NRGBA via imaging.Clone", filepath.Base(imagePath), formatName)
		decodedImg = imaging.Clone(img) // imaging.Clone should return an *image.NRGBA
	}

	// Explicitly convert to image.NRGBA to ensure 8-bit depth and compatible color model for gopdf
	bounds := decodedImg.Bounds()
	finalImg := image.NewNRGBA(bounds)
	draw.Draw(finalImg, bounds, decodedImg, bounds.Min, draw.Src)

	return finalImg, nil
}
