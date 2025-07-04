package main

import (
	"flag"
	"fmt"
	"log"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"bytes" // Added for PNG encoding buffer
	"image" // Added for image manipulation
	"image/draw" // Added for drawing images
	"image/png" // Added for PNG encoding

	"github.com/signintech/gopdf"
	"golang.org/x/image/webp"
)

func main() {
	// Define command-line flags for input and output paths
	inputDir := flag.String("i", ".", "Input directory containing .webp files")
	outputFile := flag.String("o", "output.pdf", "Output PDF file name")
	flag.Parse()

	// Call the main conversion function
	err := convertWebPToPDF(*inputDir, *outputFile)
	if err != nil {
		log.Fatalf("❌ Failed to convert images to PDF: %v", err)
	}

	fmt.Printf("✅ Successfully created '%s' from images in '%s'\n", *outputFile, *inputDir)
}

// convertWebPToPDF finds all .webp files, decodes them, and adds them to a PDF.
func convertWebPToPDF(inputDir, outputFile string) error {
	// 1. Read all files from the input directory
	files, err := os.ReadDir(inputDir)
	if err != nil {
		return fmt.Errorf("could not read directory %s: %w", inputDir, err)
	}

	// 2. Filter for .webp files and store their names
	var webpFiles []string
	for _, file := range files {
		if !file.IsDir() && strings.HasSuffix(strings.ToLower(file.Name()), ".webp") {
			webpFiles = append(webpFiles, file.Name())
		}
	}

	if len(webpFiles) == 0 {
		return fmt.Errorf("no .webp files found in directory %s", inputDir)
	}

	// 3. Sort the files alphabetically
	sort.Strings(webpFiles)
	fmt.Printf("Found %d .webp files to convert.\n", len(webpFiles))

	// 4. Initialize a new PDF document using gopdf
	pdf := gopdf.GoPdf{}
	pdf.Start(gopdf.Config{PageSize: *gopdf.PageSizeA4}) // Default, will be overridden

	// 5. Process images concurrently and add them to the PDF sequentially.

	// Define a struct to hold processed image data and its original index for ordering.
	type ProcessedImage struct {
		Index      int
		Filename   string
		Image      image.Image
		Error      error
	}

	// Number of concurrent decoders. Let's use a reasonable number, e.g., number of CPUs or a fixed value.
	// For simplicity, let's use a fixed number like 4. This can be tuned.
	// runtime.NumCPU() could also be used.
	maxConcurrentDecoders := 4

	// Create a channel to send processed image data.
	// The buffer size is len(webpFiles) to prevent blocking if PDF processing is slow,
	// though with a semaphore, this might not be strictly necessary if workers block on semaphore.
	// Let's make it unbuffered and rely on the semaphore for backpressure.
	processedImageChan := make(chan ProcessedImage)
	// Create a semaphore channel to limit concurrency.
	semaphoreChan := make(chan struct{}, maxConcurrentDecoders)

	// Launch goroutines to decode images.
	for i, filename := range webpFiles {
		go func(idx int, fname string) {
			semaphoreChan <- struct{}{} // Acquire a slot
			defer func() { <-semaphoreChan }() // Release the slot

			fullPath := filepath.Join(inputDir, fname)
			fmt.Printf("Decoding: %s\n", fname)

			file, err := os.Open(fullPath)
			if err != nil {
				processedImageChan <- ProcessedImage{Index: idx, Filename: fname, Error: fmt.Errorf("could not open file: %w", err)}
				return
			}
			defer file.Close()

			decodedImg, err := webp.Decode(file)
			if err != nil {
				processedImageChan <- ProcessedImage{Index: idx, Filename: fname, Error: fmt.Errorf("could not decode WebP: %w", err)}
				return
			}

			processedImageChan <- ProcessedImage{Index: idx, Filename: fname, Image: decodedImg}
		}(i, filename)
	}

	// Collect results and add to PDF.
	// We need to store them temporarily to ensure correct order if they arrive out of order.
	results := make([]ProcessedImage, len(webpFiles))
	for i := 0; i < len(webpFiles); i++ {
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

	// 6. Write the final PDF to the specified output file.
	err = pdf.WritePdf(outputFile)
	if err != nil {
		return fmt.Errorf("could not save PDF file: %w", err)
	}

	return nil
}
