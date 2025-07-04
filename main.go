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

	// 5. Loop through each WebP file and add it to the PDF
	for _, filename := range webpFiles {
		fullPath := filepath.Join(inputDir, filename)
		fmt.Printf("Processing: %s\n", filename)

		file, err := os.Open(fullPath)
		if err != nil {
			log.Printf("... ⚠️  could not open file %s: %v. Skipping.", filename, err)
			continue
		}

		img, err := webp.Decode(file)
		if err != nil {
			file.Close()
			log.Printf("... ⚠️  could not decode WebP file %s: %v. Skipping.", filename, err)
			continue
		}
		file.Close()

		// Encode the image.Image to PNG format into a bytes.Buffer
		var pngBuffer bytes.Buffer
		err = png.Encode(&pngBuffer, img)
		if err != nil {
			log.Printf("... ⚠️  could not encode image %s to PNG: %v. Skipping.", filename, err)
			continue
		}

		// Create ImageHolder from the reader
		holder, err := gopdf.ImageHolderByReader(&pngBuffer)
		if err != nil {
			log.Printf("... ⚠️  could not create image holder for %s: %v. Skipping.", filename, err)
			continue
		}

		width := float64(img.Bounds().Dx())
		height := float64(img.Bounds().Dy())

		// Add a new page with the precise dimensions of the image
		pageOptions := gopdf.PageOption{
			PageSize: &gopdf.Rect{W: width, H: height},
		}
		pdf.AddPageWithOption(pageOptions)

		// Draw the image onto the page.
		err = pdf.ImageByHolder(holder, 0, 0, &gopdf.Rect{W: width, H: height})
		if err != nil {
			log.Printf("... ⚠️  could not draw image %s on PDF: %v. Skipping.", filename, err)
			continue
		}
	}

	// 6. Write the final PDF to the specified output file
	err = pdf.WritePdf(outputFile)
	if err != nil {
		return fmt.Errorf("could not save PDF file: %w", err)
	}

	return nil
}
