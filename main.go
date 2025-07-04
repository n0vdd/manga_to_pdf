package main

import (
	"flag"
	"fmt"
	"log"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync" // Added for sync.WaitGroup
	"image" // Added for image manipulation
	"image/draw" // Added for explicit conversion to NRGBA
	_ "image/jpeg" // Added for JPEG decoding (register decoder)
	_ "image/png" // Added for PNG encoding (register decoder)

	"github.com/disintegration/imaging" // Added for image conversion
	"github.com/signintech/gopdf"
	_ "golang.org/x/image/webp" // Added for WebP decoding (register decoder)
)

func main() {
	// Define command-line flags for input and output paths
	inputDir := flag.String("i", ".", "Input directory containing image files (.webp, .jpg, .jpeg, .png)")
	outputFile := flag.String("o", "output.pdf", "Output PDF file name")
	flag.Parse()

	// Call the main conversion function
	err := convertImagesToPDF(*inputDir, *outputFile)
	if err != nil {
		log.Fatalf("❌ Failed to convert images to PDF: %v", err)
	}

	fmt.Printf("✅ Successfully created '%s' from images in '%s'\n", *outputFile, *inputDir)
}

// convertImagesToPDF finds all supported image files, decodes them, and adds them to a PDF.
func convertImagesToPDF(inputDir, outputFile string) error {
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
	fmt.Printf("Found %d image files to convert.\n", len(imageFiles))

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

	maxConcurrentDecoders := 4 // Number of concurrent image processing goroutines

	processedResults := make([]ProcessedImage, len(imageFiles))
	var wg sync.WaitGroup
	semaphore := make(chan struct{}, maxConcurrentDecoders)

	// Launch goroutines to process images.
	for i, filename := range imageFiles {
		wg.Add(1)
		go func(idx int, fname string) {
			defer wg.Done()
			semaphore <- struct{}{}        // Acquire a slot
			defer func() { <-semaphore }() // Release the slot

			fullPath := filepath.Join(inputDir, fname)
			log.Printf("Processing: %s", fname)

			img, err := processImage(fullPath)
			if err != nil {
				processedResults[idx] = ProcessedImage{Index: idx, Filename: fname, Error: fmt.Errorf("processing image %s failed: %w", fname, err)}
				return
			}
			processedResults[idx] = ProcessedImage{Index: idx, Filename: fname, Image: img}
		}(i, filename)
	}

	wg.Wait()
	close(semaphore)

	// Add images to PDF in the original sorted order.
	for _, res := range processedResults {
		if res.Error != nil {
			log.Printf("Skipping %s due to error: %v", res.Filename, res.Error)
			continue
		}

		log.Printf("Adding to PDF: %s", res.Filename)
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

// processImage handles opening, decoding, and converting a single image.
// It returns the processed image.Image or an error.
func processImage(imagePath string) (image.Image, error) {
	file, err := os.Open(imagePath)
	if err != nil {
		return nil, fmt.Errorf("could not open file: %w", err)
	}
	defer file.Close()

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
