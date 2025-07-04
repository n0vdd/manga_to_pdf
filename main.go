package main

import (
	// "flag" // Removed for web server
	"bytes"
	"fmt"
	"html/template" // Added for HTML templates
	"io"
	"log"
	"mime/multipart" // Added for multipart form parsing
	"net/http"       // Added for HTTP server
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
	// // Define command-line flags for input and output paths - REMOVED
	// inputDir := flag.String("i", ".", "Input directory containing image files (.webp, .jpg, .jpeg, .png)")
	// outputFile := flag.String("o", "output.pdf", "Output PDF file name")
	// flag.Parse()

	// // Call the main conversion function - REMOVED
	// err := convertImagesToPDF(*inputDir, *outputFile)
	// if err != nil {
	// 	log.Fatalf("❌ Failed to convert images to PDF: %v", err)
	// }
	// fmt.Printf("✅ Successfully created '%s' from images in '%s'\n", *outputFile, *inputDir)

	http.HandleFunc("/", handleIndex) // Placeholder for index handler
	http.HandleFunc("/upload", handleUpload) // Placeholder for upload handler

	log.Println("Server starting on port 8080...")
	if err := http.ListenAndServe(":8080", nil); err != nil {
		log.Fatalf("❌ Could not start server: %v", err)
	}
}

var tmpl = template.Must(template.ParseFiles("templates/index.html"))

// handleIndex will serve the main HTML page.
func handleIndex(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		http.Error(w, "Method not allowed", http.StatusMethodNotAllowed)
		return
	}
	err := tmpl.Execute(w, nil)
	if err != nil {
		log.Printf("Error executing template: %v", err)
		http.Error(w, "Internal Server Error", http.StatusInternalServerError)
		return
	}
	log.Println("Served index page.")
}

const MAX_UPLOAD_SIZE = 100 * 1024 * 1024 // 100 MB
const MAX_FILES = 500 // Max number of files in a single "folder" upload

// handleUpload will process uploaded images and return a PDF.
func handleUpload(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "Method not allowed", http.StatusMethodNotAllowed)
		return
	}

	// Enforce maximum upload size
	r.Body = http.MaxBytesReader(w, r.Body, MAX_UPLOAD_SIZE)
	if err := r.ParseMultipartForm(MAX_UPLOAD_SIZE); err != nil {
		log.Printf("Error parsing multipart form: %v", err)
		http.Error(w, fmt.Sprintf("Upload too large. Max size is %dMB.", MAX_UPLOAD_SIZE/(1024*1024)), http.StatusBadRequest)
		return
	}

	// Create a temporary directory to store uploaded files
	tempDir, err := os.MkdirTemp("", "imageuploads-*-pdf")
	if err != nil {
		log.Printf("Error creating temp directory: %v", err)
		http.Error(w, "Internal Server Error", http.StatusInternalServerError)
		return
	}
	defer func() {
		log.Printf("Removing temporary directory: %s", tempDir)
		if err := os.RemoveAll(tempDir); err != nil {
			log.Printf("Error removing temp directory %s: %v", tempDir, err)
		}
	}()

	// "files" is the name of the input field in the HTML form
	formFiles := r.MultipartForm.File["files"]
	if len(formFiles) == 0 {
		log.Println("No files uploaded.")
		http.Error(w, "No files uploaded. Please select a folder with images.", http.StatusBadRequest)
		return
	}
	if len(formFiles) > MAX_FILES {
		log.Printf("Too many files uploaded: %d, max is %d", len(formFiles), MAX_FILES)
		http.Error(w, fmt.Sprintf("Too many files. Max %d files allowed.", MAX_FILES), http.StatusBadRequest)
		return
	}


	log.Printf("Received %d files. Storing in %s", len(formFiles), tempDir)

	for _, fileHeader := range formFiles {
		// Sanitize filename (important!)
		// For webkitdirectory uploads, the filename includes the relative path.
		// We need to ensure it's safe and doesn't try to escape the tempDir.
		// filepath.Join will clean it, but we also need to ensure it's just a filename.
		// And more importantly, ensure the full path is within tempDir.
		fileName := filepath.Base(fileHeader.Filename) // Use only the base name for security.
		// The fileHeader.Filename can contain path components for directory uploads.
		// We must be careful here. The `webkitdirectory` attribute often sends relative paths.
		// Let's ensure we are only using the leaf part of the path for the file name inside our tempDir.
		// And ensure the original name doesn't contain ".." or other traversal sequences.
		if strings.Contains(fileHeader.Filename, "..") {
			log.Printf("Invalid filename received: %s", fileHeader.Filename)
			http.Error(w, "Invalid filename in upload.", http.StatusBadRequest)
			return
		}

		// Reconstruct path within tempDir safely.
		// If fileHeader.Filename contains subdirectories like "subdir/image.png",
		// we need to create "tempDir/subdir/" first.
		relPath := filepath.Clean(fileHeader.Filename)
		if strings.HasPrefix(relPath, "/") || strings.HasPrefix(relPath, "..") {
			log.Printf("Potentially malicious path in upload: %s", fileHeader.Filename)
			http.Error(w, "Invalid file path in upload.", http.StatusBadRequest)
			return
		}

		targetPath := filepath.Join(tempDir, relPath)

		// Create subdirectories if they are part of the uploaded structure
		if err := os.MkdirAll(filepath.Dir(targetPath), os.ModePerm); err != nil {
			log.Printf("Error creating subdirectory for %s: %v", targetPath, err)
			http.Error(w, "Internal server error creating subdirectories for upload.", http.StatusInternalServerError)
			return
		}


		srcFile, err := fileHeader.Open()
		if err != nil {
			log.Printf("Error opening uploaded file %s: %v", fileHeader.Filename, err)
			http.Error(w, "Error processing uploaded file", http.StatusInternalServerError)
			return
		}
		defer srcFile.Close()

		dstFile, err := os.Create(targetPath)
		if err != nil {
			log.Printf("Error creating destination file %s: %v", targetPath, err)
			http.Error(w, "Error saving uploaded file", http.StatusInternalServerError)
			return
		}
		defer dstFile.Close()

		if _, err := io.Copy(dstFile, srcFile); err != nil {
			log.Printf("Error copying uploaded file %s to %s: %v", fileHeader.Filename, targetPath, err)
			http.Error(w, "Error saving uploaded file content", http.StatusInternalServerError)
			return
		}
		log.Printf("Saved %s to %s", fileHeader.Filename, targetPath)
	}

	// All files are now in tempDir. Proceed to PDF conversion.
	log.Printf("Starting PDF conversion for files in %s", tempDir)

	// Set headers before writing to the response writer.
	// Content-Length cannot be reliably set before generation when streaming.
	// The browser will use chunked transfer encoding or close the connection when done.
	w.Header().Set("Content-Type", "application/pdf")
	w.Header().Set("Content-Disposition", "attachment; filename=\"converted_images.pdf\"")

	bytesWritten, err := convertImagesToPDF(tempDir, w)
	if err != nil {
		log.Printf("Error converting images to PDF and streaming: %v", err)
		// Important: If headers are already sent, we can't send a different HTTP error code.
		// We attempt to send an error, but if `w` has been written to, this might not work as expected.
		// The client might receive a truncated PDF or a connection error.
		if strings.Contains(err.Error(), "no supported image files") {
			// This check might be too late if PDF generation started and failed mid-way.
			// Consider sending a plain text error if no bytes have been written yet.
			http.Error(w, "No supported image files (.webp, .jpg, .jpeg, .png) found in the uploaded folder.", http.StatusBadRequest)
		} else {
			http.Error(w, "Error generating PDF.", http.StatusInternalServerError)
		}
		return
	}

	if bytesWritten == 0 {
		log.Println("Generated PDF is empty (0 bytes written).")
		// If no bytes were written, we can still send an HTTP error.
		http.Error(w, "Generated PDF is empty.", http.StatusInternalServerError)
		return
	}

	log.Printf("PDF generated and streamed successfully, size: %d bytes.", bytesWritten)
	// No explicit io.Copy is needed here as convertImagesToPDF now writes directly to w.
	// The log "PDF sent successfully." is implicit if bytesWritten > 0 and no error occurred.
}


// convertImagesToPDF finds all supported image files, decodes them, and adds them to a PDF.
// It now writes the PDF directly to an io.Writer.
func convertImagesToPDF(inputDir string, writer io.Writer) (int64, error) {
	// 1. Read all files from the input directory
	files, err := os.ReadDir(inputDir)
	if err != nil {
		return 0, fmt.Errorf("could not read directory %s: %w", inputDir, err)
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
		return 0, fmt.Errorf("no supported image files (.webp, .jpg, .jpeg, .png) found in directory %s", inputDir)
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

	// 6. Write the final PDF directly to the provided io.Writer.
	n, err := pdf.WriteTo(writer)
	if err != nil {
		return 0, fmt.Errorf("could not write PDF to writer: %w", err)
	}

	return n, nil
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
