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
	"context" // Added for graceful shutdown
	"image/draw"                // Added for explicit conversion to NRGBA
	_ "image/jpeg"              // Added for JPEG decoding (register decoder)
	_ "image/png"               // Added for PNG encoding (register decoder)
	"io"
	"log/slog" // Standard library structured logging
	"os"
	"os/signal" // Added for graceful shutdown
	"path/filepath"
	"runtime" // Added for memory profiling
	"runtime/pprof" // Added for CPU and memory profiling
	"sort"
	"strings"
	"sync" // Added for sync.Pool
)

// bufferPool is used to reuse byte buffers for WEBP to JPG conversion.
var bufferPool = sync.Pool{
	New: func() interface{} {
		return new(bytes.Buffer)
	},
}

// ErrNoSupportedFiles is returned when no supported image files are found in a directory.
var ErrNoSupportedFiles = errors.New("no supported image files found")

// ProcessedImage holds the data for an image that has been processed and is ready for PDF registration.
type ProcessedImage struct {
	Index           int    // Original index of the file, for ordering
	Filename        string // Original filename
	Error           error  // Error encountered during processing
	Reader          io.Reader // Reader for image data (either *os.File or *bytes.Buffer)
	Width           float64   // Width of the image in points
	Height          float64   // Height of the image in points
	ImageTypeForPDF string    // Type string for gofpdf ("PNG", "JPG")
}

// Config holds all application configuration.
type Config struct {
	InputDirectory string
	OutputFilename string
	CPUProfileFile string
	MemProfileFile string
	NumWorkers     int
	JPEGQuality    int
	VerboseLogging bool
}

func main() {
	cfg := Config{}
	flag.StringVar(&cfg.InputDirectory, "i", ".", "Input directory containing image files (.webp, .jpg, .jpeg, .png)")
	flag.StringVar(&cfg.OutputFilename, "o", "output.pdf", "Output PDF file name")
	flag.StringVar(&cfg.CPUProfileFile, "cpuprofile", "", "Write cpu profile to `file`")
	flag.StringVar(&cfg.MemProfileFile, "memprofile", "", "Write memory profile to `file`")
	flag.IntVar(&cfg.NumWorkers, "concurrency", runtime.NumCPU(), "Number of concurrent workers for image processing")
	flag.IntVar(&cfg.JPEGQuality, "jpeg-quality", 90, "JPEG quality for WEBP conversion (1-100)")
	flag.BoolVar(&cfg.VerboseLogging, "verbose", false, "Enable verbose/debug logging")
	flag.Parse()

	// Setup structured logger
	var logLevel slog.Level
	if cfg.VerboseLogging {
		logLevel = slog.LevelDebug
	} else {
		logLevel = slog.LevelInfo
	}
	logger := slog.New(slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{Level: logLevel}))
	slog.SetDefault(logger)

	// Validate and apply defaults for config values
	if cfg.NumWorkers <= 0 {
		slog.Warn("Concurrency must be a positive number, defaulting to NumCPU", "provided", cfg.NumWorkers, "default", runtime.NumCPU())
		cfg.NumWorkers = runtime.NumCPU()
	}
	if cfg.JPEGQuality < 1 || cfg.JPEGQuality > 100 {
		slog.Warn("JPEG quality must be between 1 and 100, defaulting to 90", "provided", cfg.JPEGQuality, "default", 90)
		cfg.JPEGQuality = 90
	}

	if cfg.CPUProfileFile != "" {
		f, err := os.Create(cfg.CPUProfileFile)
		if err != nil {
			slog.Error("could not create CPU profile", "file", cfg.CPUProfileFile, "error", err)
			os.Exit(1)
		}
		defer f.Close()
		if err := pprof.StartCPUProfile(f); err != nil {
			slog.Error("could not start CPU profile", "error", err)
			os.Exit(1)
		}
		defer pprof.StopCPUProfile()
		slog.Info("CPU profiling enabled", "file", cfg.CPUProfileFile)
	}

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	sigChan := make(chan os.Signal, 1)
	signal.Notify(sigChan, os.Interrupt)
	go func() {
		sig := <-sigChan
		slog.Info("Received signal, shutting down gracefully...", "signal", sig)
		cancel()
	}()

	err := runApp(ctx, &cfg) // Pass pointer to Config
	if err != nil {
		if errors.Is(err, context.Canceled) {
			slog.Info("Process canceled by user. Output might be incomplete.")
			os.Exit(130)
		} else if !errors.Is(err, ErrNoSupportedFiles) {
			slog.Error("Application run failed", "error", err)
			os.Exit(1)
		}
		// If ErrNoSupportedFiles, runApp handles logging and cleanup.
	}

	if cfg.MemProfileFile != "" {
		f, err := os.Create(cfg.MemProfileFile)
		if err != nil {
			slog.Error("could not create memory profile", "file", cfg.MemProfileFile, "error", err)
			os.Exit(1)
		}
		defer f.Close()
		runtime.GC()
		if err := pprof.WriteHeapProfile(f); err != nil {
			slog.Error("could not write memory profile", "error", err)
			os.Exit(1)
		}
		slog.Info("Memory profile written", "file", cfg.MemProfileFile)
	}
}

func runApp(ctx context.Context, cfg *Config) error {
	select {
	case <-ctx.Done():
		slog.Info("runApp: cancellation detected before starting.")
		if _, statErr := os.Stat(cfg.OutputFilename); statErr == nil {
			if removeErr := os.Remove(cfg.OutputFilename); removeErr != nil {
				slog.Warn("Failed to remove output file during early cancellation", "file", cfg.OutputFilename, "error", removeErr)
			} else {
				slog.Info("Removed output file due to early cancellation", "file", cfg.OutputFilename)
			}
		}
		return ctx.Err()
	default:
	}

	outFile, err := os.Create(cfg.OutputFilename)
	if err != nil {
		return fmt.Errorf("could not create output file %s: %w", cfg.OutputFilename, err)
	}

	hasContent, conversionErr := convertImagesToPDF(ctx, cfg, outFile)

	if closeErr := outFile.Close(); closeErr != nil {
		slog.Warn("Failed to close output file", "file", cfg.OutputFilename, "error", closeErr)
		if conversionErr == nil {
			conversionErr = fmt.Errorf("failed to close output file %s: %w", cfg.OutputFilename, closeErr)
		}
	}

	if conversionErr != nil {
		if errors.Is(conversionErr, context.Canceled) {
			slog.Info("PDF conversion canceled.", "inputDir", cfg.InputDirectory, "outputFile", cfg.OutputFilename)
			if removeErr := os.Remove(cfg.OutputFilename); removeErr != nil {
				slog.Warn("Failed to remove output file after cancellation", "file", cfg.OutputFilename, "error", removeErr)
			} else {
				slog.Debug("Removed output file after cancellation", "file", cfg.OutputFilename)
			}
			return context.Canceled
		}
		slog.Error("Failed to convert images to PDF", "inputDir", cfg.InputDirectory, "error", conversionErr)
		if removeErr := os.Remove(cfg.OutputFilename); removeErr != nil {
			slog.Warn("Failed to remove output file after error", "file", cfg.OutputFilename, "error", removeErr)
		} else {
			slog.Debug("Removed output file after error", "file", cfg.OutputFilename)
		}
		return conversionErr
	}

	if !hasContent {
		slog.Info("No images were successfully added to the PDF. Output file removed.", "inputDir", cfg.InputDirectory, "outputFile", cfg.OutputFilename)
		if removeErr := os.Remove(cfg.OutputFilename); removeErr != nil {
			slog.Warn("Failed to remove output file after no content", "file", cfg.OutputFilename, "error", removeErr)
		}
		return nil
	}

	slog.Info("Successfully created PDF", "outputFile", cfg.OutputFilename, "inputDir", cfg.InputDirectory)
	return nil
}

func findSupportedImageFiles(inputDir string) ([]string, error) {
	slog.Debug("Scanning for supported image files", "directory", inputDir)
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
		slog.Info("No supported image files found", "directory", inputDir)
		return nil, fmt.Errorf("%w in directory %s", ErrNoSupportedFiles, inputDir)
	}

	sort.Strings(imageFiles)
	slog.Debug("Found supported image files", "count", len(imageFiles), "directory", inputDir)
	return imageFiles, nil
}

func processImageAndRegister(ctx context.Context, cfg *Config, filename string, idx int) ProcessedImage {
	slog.Debug("Starting to process image", "filename", filename, "index", idx)
	select {
	case <-ctx.Done():
		slog.Debug("Context cancelled before processing image", "filename", filename)
		return ProcessedImage{Index: idx, Filename: filename, Error: ctx.Err()}
	default:
	}

	fullPath := filepath.Join(cfg.InputDirectory, filename)
	ext := strings.ToLower(filepath.Ext(filename))
	processedInfo := ProcessedImage{Index: idx, Filename: filename}

	file, err := os.Open(fullPath)
	if err != nil {
		processedInfo.Error = fmt.Errorf("could not open file %s: %w", fullPath, err)
		return processedInfo
	}
	defer file.Close()

	var imgConfig image.Config
	var formatName string
	var imageType string

	if ext == ".png" || ext == ".jpg" || ext == ".jpeg" {
		slog.Debug("Processing as PNG/JPG (direct reader)", "filename", filename)
		imgConfig, formatName, err = image.DecodeConfig(file)
		if err != nil {
			processedInfo.Error = fmt.Errorf("could not decode image config for %s: %w", filename, err)
			return processedInfo
		}
		if _, err = file.Seek(0, io.SeekStart); err != nil {
			processedInfo.Error = fmt.Errorf("could not seek file %s: %w", filename, err)
			return processedInfo
		}
		imageType = strings.ToUpper(strings.TrimPrefix(ext, "."))
		if imageType == "JPEG" {
			imageType = "JPG"
		}
		processedInfo.Reader = file
		processedInfo.Width = float64(imgConfig.Width)
		processedInfo.Height = float64(imgConfig.Height)
		processedInfo.ImageTypeForPDF = imageType
	} else if ext == ".webp" {
		slog.Debug("Processing as WEBP (decode and re-encode to JPG)", "filename", filename)
		decodedImg, webpFormatName, err := image.Decode(file)
		if err != nil {
			processedInfo.Error = fmt.Errorf("could not decode webp image %s: %w", filename, err)
			return processedInfo
		}
		formatName = webpFormatName
		switch decodedImg.(type) {
		case *image.Gray16, *image.NRGBA64, *image.RGBA64:
			slog.Debug("Converting 16-bit WebP image to 8-bit NRGBA", "filename", filename)
			decodedImg = imaging.Clone(decodedImg)
		}
		buf := bufferPool.Get().(*bytes.Buffer)
		buf.Reset()
		if err := imaging.Encode(buf, decodedImg, imaging.JPEG, imaging.JPEGQuality(cfg.JPEGQuality)); err != nil {
			bufferPool.Put(buf)
			processedInfo.Error = fmt.Errorf("could not re-encode webp %s to jpg: %w", filename, err)
			return processedInfo
		}
		processedInfo.Reader = buf
		processedInfo.Width = float64(decodedImg.Bounds().Dx())
		processedInfo.Height = float64(decodedImg.Bounds().Dy())
		processedInfo.ImageTypeForPDF = "JPG"
	} else {
		processedInfo.Error = fmt.Errorf("unsupported file type by processImageAndRegister: %s", ext)
		return processedInfo
	}
	slog.Debug("Successfully processed image", "filename", filename, "originalFormat", formatName, "pdfType", imageType, "width", processedInfo.Width, "height", processedInfo.Height)
	return processedInfo
}

func processImagesConcurrently(ctx context.Context, cfg *Config, imageFiles []string) []ProcessedImage {
	slog.Debug("Starting concurrent image processing", "numFiles", len(imageFiles), "numWorkers", cfg.NumWorkers)
	if len(imageFiles) == 0 {
		return []ProcessedImage{}
	}

	processedImageChan := make(chan ProcessedImage)
	semaphoreChan := make(chan struct{}, cfg.NumWorkers)
	var wg sync.WaitGroup
	results := make([]ProcessedImage, len(imageFiles))

	for i, filename := range imageFiles {
		select {
		case <-ctx.Done():
			slog.Info("Cancellation detected before starting all goroutines", "lastProcessedIndex", i-1, "filename", filename)
			for j := i; j < len(imageFiles); j++ {
				if results[j].Filename == "" {
					results[j] = ProcessedImage{Index: j, Filename: imageFiles[j], Error: ctx.Err()}
				}
			}
			goto endGoroutineLoop
		default:
		}

		wg.Add(1)
		go func(idx int, fname string) {
			defer wg.Done()
			slog.Debug("Goroutine started for image", "filename", fname, "index", idx)
			select {
			case semaphoreChan <- struct{}{}:
				defer func() { <-semaphoreChan }()
			case <-ctx.Done():
				slog.Debug("Cancellation detected before acquiring semaphore", "filename", fname)
				processedImageChan <- ProcessedImage{Index: idx, Filename: fname, Error: ctx.Err()}
				return
			}

			processedResult := processImageAndRegister(ctx, cfg, fname, idx)
			select {
			case processedImageChan <- processedResult:
			case <-ctx.Done():
				slog.Debug("Cancellation detected while trying to send result", "filename", fname)
				if processedResult.Error == nil {
					processedResult.Error = ctx.Err()
				}
				if closer, ok := processedResult.Reader.(io.Closer); ok {
					closer.Close()
				} else if buf, ok := processedResult.Reader.(*bytes.Buffer); ok {
					bufferPool.Put(buf)
				}
				processedImageChan <- processedResult // Attempt to send anyway for accounting
			}
		}(i, filename)
	}

endGoroutineLoop:
	go func() {
		wg.Wait()
		close(processedImageChan)
		close(semaphoreChan)
		slog.Debug("All image processing goroutines completed.")
	}()

	for i := 0; i < len(imageFiles); i++ {
		select {
		case res, ok := <-processedImageChan:
			if ok {
				results[res.Index] = res
			} else { // Channel closed
				slog.Debug("Processed image channel closed.")
				// Fill remaining with cancellation error if context is done
				if ctx.Err() != nil {
					for k := 0; k < len(imageFiles); k++ {
						if results[k].Filename == "" {
							results[k] = ProcessedImage{Index: k, Filename: imageFiles[k], Error: ctx.Err()}
						}
					}
				}
				goto endCollectionLoop
			}
		case <-ctx.Done():
			slog.Info("Cancellation detected while collecting results.")
			for j := 0; j < len(imageFiles); j++ {
				if results[j].Filename == "" {
					results[j] = ProcessedImage{Index: j, Filename: imageFiles[j], Error: ctx.Err()}
				}
			}
			goto endCollectionLoop
		}
	}
endCollectionLoop:
	if ctx.Err() != nil { // Drain channel if cancelled to let goroutines finish sending
		for range processedImageChan {
		}
	}
	slog.Debug("Finished collecting image processing results.")
	return results
}

func generatePDFFromProcessedImages(ctx context.Context, writer io.Writer, processedImages []ProcessedImage, pdf *gofpdf.Fpdf) (hasContent bool, err error) {
	slog.Debug("Starting PDF generation from processed images", "numImages", len(processedImages))
	hasContent = false
	for i, res := range processedImages {
		select {
		case <-ctx.Done():
			slog.Info("Cancellation detected before adding image to PDF", "filename", res.Filename)
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
				slog.Debug("Skipping image due to earlier cancellation", "filename", res.Filename)
			} else {
				slog.Warn("Skipping image due to error during its processing", "filename", res.Filename, "error", res.Error)
			}
			if closer, ok := res.Reader.(io.Closer); ok {
				closer.Close()
			} else if buf, ok := res.Reader.(*bytes.Buffer); ok {
				bufferPool.Put(buf)
			}
			continue
		}
		if res.Reader == nil {
			slog.Warn("Reader for image is nil, skipping", "filename", res.Filename)
			continue
		}

		slog.Debug("Adding image to PDF", "filename", res.Filename, "width", res.Width, "height", res.Height, "type", res.ImageTypeForPDF)
		readerToClean := res.Reader
		pdf.AddPageFormat("P", gofpdf.SizeType{Wd: res.Width, Ht: res.Height})
		if pdf.Err() {
			slog.Warn("Could not add page to PDF for image", "filename", res.Filename, "error", pdf.Error())
			pdf.ClearError()
			if closer, ok := readerToClean.(io.Closer); ok {
				closer.Close()
			} else if buf, ok := readerToClean.(*bytes.Buffer); ok {
				bufferPool.Put(buf)
			}
			continue
		}

		imageName := fmt.Sprintf("image%d", i)
		pdf.RegisterImageOptionsReader(imageName, gofpdf.ImageOptions{ImageType: res.ImageTypeForPDF, ReadDpi: false}, res.Reader)
		if fCloser, ok := readerToClean.(*os.File); ok {
			fCloser.Close()
		} else if bReader, ok := readerToClean.(*bytes.Buffer); ok {
			bufferPool.Put(bReader)
		}

		if pdf.Err() {
			slog.Warn("Could not register image in PDF", "filename", res.Filename, "error", pdf.Error())
			pdf.ClearError()
			continue
		}

		pdf.ImageOptions(imageName, 0, 0, res.Width, res.Height, false, gofpdf.ImageOptions{ImageType: res.ImageTypeForPDF}, 0, "")
		if pdf.Err() {
			slog.Warn("Could not place image on PDF page", "filename", res.Filename, "error", pdf.Error())
			pdf.ClearError()
			continue
		}
		hasContent = true
		slog.Debug("Successfully added image to PDF", "filename", res.Filename)
	}

	if pdf.Err() {
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
		if ctx.Err() != nil {
			return false, ctx.Err()
		}
		if len(processedImages) > 0 {
			slog.Info("No content was added to the PDF (all images skipped or failed).")
		} else {
			slog.Info("No images processed and no content to add to PDF.")
		}
	}
	return hasContent, nil
}

func convertImagesToPDF(ctx context.Context, cfg *Config, writer io.Writer) (hasContent bool, err error) {
	slog.Debug("Starting PDF conversion process", "inputDir", cfg.InputDirectory)
	select {
	case <-ctx.Done():
		return false, ctx.Err()
	default:
	}

	imageFiles, err := findSupportedImageFiles(cfg.InputDirectory)
	if err != nil {
		return false, err
	}
	select {
	case <-ctx.Done():
		return false, ctx.Err()
	default:
	}
	if len(imageFiles) == 0 {
		return false, ErrNoSupportedFiles // Should be caught by findSupportedImageFiles, but defensive.
	}
	slog.Info("Found image files to convert", "count", len(imageFiles), "inputDir", cfg.InputDirectory)

	pdf := gofpdf.New("P", "pt", "A4", "")
	processedImageInfos := processImagesConcurrently(ctx, cfg, imageFiles)

	select {
	case <-ctx.Done():
		slog.Info("Cancellation detected before PDF generation phase.")
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
	return generatePDFFromProcessedImages(ctx, writer, processedImageInfos, pdf)
}
