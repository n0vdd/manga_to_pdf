# Manga/Image to PDF Converter (Go)

A command-line utility written in Go to convert a directory of images (WEBP, JPG, PNG) into a single PDF document. This tool is particularly useful for archiving image sequences, such as manga chapters or scanned documents.

## Features

*   **Supported Formats**: Converts WEBP, JPG/JPEG, and PNG images.
*   **Image Sorting**: Images are sorted alphanumerically by filename before being added to the PDF, ensuring correct order.
*   **Image Processing**: Handles 16-bit depth images by converting them to 8-bit NRGBA for compatibility with PDF generation.
*   **Concurrent Processing**: Decodes and processes multiple images concurrently to potentially speed up the conversion process, especially for a large number of files.
*   **Customizable Output**: Allows users to specify the input directory containing the images and the desired output filename for the PDF.
*   **Error Handling**: Provides feedback on processing errors, skips problematic images, and attempts to create a PDF with successfully processed images.

## Dependencies

The project relies on the following Go packages:

*   `github.com/disintegration/imaging v1.6.2`: For advanced image processing tasks, including resizing and format handling.
*   `github.com/jung-kurt/gofpdf v0.0.0-20191119144553-603f56990463`: For PDF generation. (As listed in `go.mod`)
*   `golang.org/x/image v0.28.0`: For decoding various image formats (WEBP, PNG, JPEG).

## Installation

### Option 1: Using `go install` (Recommended for users)

If the repository is public and you have Go installed:
```bash
go install github.com/your-username/manga_to_pdf@latest
# Ensure your GOPATH/bin or GOBIN is in your system's PATH
# Then you can run: manga_to_pdf -i ...
```
*(Replace `github.com/your-username/manga_to_pdf` with the actual repository path once it's hosted.)*

### Option 2: Building from Source (Recommended for developers)

1.  **Clone the repository:**
    ```bash
    git clone https://github.com/your-username/manga_to_pdf.git
    cd manga_to_pdf
    ```
    *(Replace `https://github.com/your-username/manga_to_pdf.git` with the actual repository URL.)*

2.  **Build the executable:**
    ```bash
    go build
    ```
    This will create an executable named `manga_to_pdf` (or `manga_to_pdf.exe` on Windows) in the project directory.

## Usage

Run the compiled executable from your terminal, specifying the input directory and output file:

```bash
./manga_to_pdf -i /path/to/your/images -o my_document.pdf
```

Or, if installed via `go install`:
```bash
manga_to_pdf -i /path/to/your/images -o my_document.pdf
```

### Command-Line Flags:

*   `-i <directory>`: Specifies the input directory containing the image files. Defaults to the current directory (`.`).
*   `-o <filename>`: Specifies the name of the output PDF file. Defaults to `output.pdf`.
*   `-cpuprofile <file>`: Write CPU profile to the specified `file`.
*   `-memprofile <file>`: Write memory profile to the specified `file`.

**Example:**

To convert images from a folder named `manga_chapter_1` into a PDF named `chapter1.pdf`:
```bash
./manga_to_pdf -i ./manga_chapter_1 -o chapter1.pdf
```

### Profiling

To analyze performance, you can generate CPU or memory profiles:

*   **CPU Profiling**:
    ```bash
    ./manga_to_pdf -i <input_dir> -o <output_file> -cpuprofile cpu.pprof
    # Then analyze with:
    go tool pprof cpu.pprof
    ```

*   **Memory Profiling**:
    ```bash
    ./manga_to_pdf -i <input_dir> -o <output_file> -memprofile mem.pprof
    # Then analyze with:
    go tool pprof mem.pprof
    ```
    Refer to the Go documentation (`go tool pprof -help`) for more information on using the pprof tool.

## Contributing

Contributions are welcome! If you'd like to contribute, please:

1.  Fork the repository.
2.  Create a new branch for your feature or bug fix (`git checkout -b feature/your-feature-name`).
3.  Make your changes.
4.  Ensure your code is formatted with `go fmt` and passes `go vet`.
5.  Commit your changes (`git commit -am 'Add some feature'`).
6.  Push to the branch (`git push origin feature/your-feature-name`).
7.  Create a new Pull Request.

## License

This project is licensed under the MIT License. See the `LICENSE` file for details.
*(Note: A `LICENSE` file should be added to the repository if one is chosen. For now, this is a placeholder.)*

## API Development (In Progress)

A key ongoing effort is to develop a web API for this conversion utility. The goal is to provide endpoints for programmatic access to the image-to-PDF functionality.

**Key API Features (Planned/Under Consideration):**

*   **Endpoint for Conversion**: An endpoint (e.g., `/convert`) that accepts a collection of images (potentially as multipart/form-data or URLs to images) and returns the generated PDF.
*   **Input Flexibility**: Support for various ways to provide images (e.g., direct upload, links to images).
*   **Configuration Options**: Allow API clients to specify options similar to the CLI (e.g., output filename, potentially image processing parameters if added in the future).
*   **Status Reporting**: For long conversions, provide a way to check the status or use asynchronous processing with callbacks/webhooks.
*   **Authentication**: Plans for simple token-based authentication for API access.
*   **Documentation**: API will be documented using OpenAPI (Swagger) specifications.

Detailed design principles and guidelines for API development can be found in `AGENTS.md`.

## Future Enhancements (CLI & Core Logic)

*   Add support for more image formats (e.g., TIFF, GIF).
*   Implement options for PDF compression and quality settings.
*   Add options for page size, orientation, and margins.
*   Further performance optimizations for both CLI and upcoming API.
*   The application now includes CPU and memory profiling capabilities via command-line flags, which will be valuable for API performance tuning.
