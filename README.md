# Image to PDF Conversion API (Go)

A web API service written in Go to convert a collection of images (WEBP, JPG, PNG) into a single PDF document. This service is useful for applications requiring programmatic PDF generation from images.

## Features

*   **API-First Design**: Provides HTTP endpoints for image to PDF conversion.
*   **Supported Input Formats**: Accepts WEBP, JPG/JPEG, and PNG images.
    *   Images can be provided as direct file uploads (`multipart/form-data`).
    *   Images can be provided as URLs (API server fetches the images).
*   **Flexible Configuration**: API clients can specify:
    *   Output PDF filename.
    *   JPEG quality for WEBP conversions or re-encoding.
    *   Number of concurrent image processing workers.
*   **Image Ordering**: Images are added to the PDF in the order they are provided (uploaded files first, then URL-sourced files by their order in the input array).
*   **Image Processing**: Handles 16-bit depth WebP images by converting them to 8-bit for PDF compatibility.
*   **Concurrent Processing**: Decodes and processes multiple images concurrently to speed up conversion.
*   **OpenAPI Documentation**: API is documented using OpenAPI 3.0 (see `openapi.yaml`).
*   **Graceful Shutdown**: The server supports graceful shutdown on interrupt signals.

## Dependencies

The project relies on the following Go packages (see `go.mod` for versions):

*   `github.com/disintegration/imaging`: For advanced image processing tasks.
*   `github.com/jung-kurt/gofpdf`: For PDF generation.
*   `golang.org/x/image`: For decoding various image formats (WEBP, PNG, JPEG).

## Getting Started

### Prerequisites

*   Go (version 1.21 or newer recommended).
*   Git.

### Installation & Running

1.  **Clone the repository:**
    ```bash
    git clone <repository-url>
    cd image-to-pdf-api # Or your repository directory name
    ```

2.  **Build the executable:**
    ```bash
    go build -o image_to_pdf_server
    ```
    This will create an executable named `image_to_pdf_server`.

3.  **Run the server:**
    ```bash
    ./image_to_pdf_server
    ```
    By default, the server listens on port `8080`.

### Configuration (Environment Variables)

The server can be configured using the following environment variables:

*   `LISTEN_ADDRESS`: The address and port for the server to listen on. Defaults to `:8080`.
    *   Example: `LISTEN_ADDRESS=":8888"`
*   `VERBOSE_LOGGING`: Set to `true` or `1` to enable verbose (debug level) logging. Defaults to `false` (info level).
    *   Example: `VERBOSE_LOGGING="true"`

## API Usage

Refer to the `openapi.yaml` specification for detailed API documentation. You can use tools like Swagger Editor or ReDoc to view this specification.

### Main Endpoint: `POST /convert`

This endpoint converts images to a PDF.

*   **Request `Content-Type`**: `multipart/form-data`
*   **Form Fields**:
    *   `images` (optional): One or more image files. Use the same field name for multiple files (e.g., `images` for each file part).
    *   `image_urls` (optional): A JSON string array of image URLs.
        *   Example: `'["http://example.com/image1.jpg", "http://example.com/image2.png"]'`
    *   `config` (optional): A JSON string object with configuration options:
        *   `output_filename` (string): Suggested name for the PDF file.
        *   `jpeg_quality` (int, 1-100): Quality for JPEG encoding (default: 90).
        *   `num_workers` (int): Number of concurrent workers (default: number of CPUs).
        *   Example: `'{"output_filename": "report.pdf", "jpeg_quality": 80}'`

*   **Successful Response (200 OK)**:
    *   `Content-Type`: `application/pdf`
    *   `Content-Disposition`: `attachment; filename="<your_output_filename.pdf>"`
    *   Body: The binary PDF data.

*   **Error Responses**:
    *   `400 Bad Request`: Invalid input (e.g., malformed JSON, missing images).
    *   `422 Unprocessable Entity`: Error during image processing or fetching.
    *   `500 Internal Server Error`: Unexpected server error.
    *   Error responses are in JSON format: `{"error": "message", "details": "..."}`.

#### Example using `curl`:

**1. Uploading files:**

```bash
curl -X POST \
  -F "images=@/path/to/your/image1.jpg" \
  -F "images=@/path/to/your/image2.png" \
  -F "config={\"output_filename\":\"my_document.pdf\"}" \
  http://localhost:8080/convert \
  -o my_document.pdf
```

**2. Using image URLs:**

```bash
curl -X POST \
  -F "image_urls=[\"https://www.google.com/images/branding/googlelogo/1x/googlelogo_light_color_272x92dp.png\"]" \
  -F "config={\"output_filename\":\"from_url.pdf\", \"jpeg_quality\": 95}" \
  http://localhost:8080/convert \
  -o from_url.pdf
```

**3. Combination of upload and URLs:**

```bash
curl -X POST \
  -F "images=@/path/to/local_image.webp" \
  -F "image_urls=[\"https_example_com_remote_image.jpg\"]" \ # Replace with actual URL
  -F "config={\"output_filename\":\"combined.pdf\"}" \
  http://localhost:8080/convert \
  -o combined.pdf
```

### Health Check Endpoint: `GET /health`

*   Returns `{"status":"ok"}` with a `200 OK` status if the service is healthy.

## Development

### Building
```bash
go build -o image_to_pdf_server
```

### Running Tests
```bash
go test ./...
```
This will run all unit and integration tests. Some tests in `api/handlers_test.go` and `internal/converter/converter_test.go` might produce more meaningful results or pass specific scenarios if small, valid `test.jpg`, `test.png`, and `test.webp` files are placed in their respective `testdata` directories (`api/testdata` and `internal/converter/testdata`). Dummy text files are used as fallbacks for basic flow testing.

### Profiling

The previous CLI version had flags for CPU and memory profiling. For the API server, Go's standard `net/http/pprof` can be integrated if needed. Uncomment the pprof routes in `main.go` and import `net/http/pprof`.

Example (after enabling in `main.go`):
```bash
# In one terminal: ./image_to_pdf_server
# In another terminal:
go tool pprof http://localhost:8080/debug/pprof/profile?seconds=30 # CPU profile
go tool pprof http://localhost:8080/debug/pprof/heap # Memory profile
```

## AGENTS.md

For AI development guidelines and project principles, refer to `AGENTS.md`.

## Future Enhancements

*   Asynchronous processing for long conversions (e.g., using job queues and status endpoints).
*   Support for more image formats (e.g., TIFF, GIF).
*   More advanced PDF options (compression, page size, orientation, margins).
*   Authentication/Authorization for API access.
*   Rate limiting.

## Contributing

Contributions are welcome! Please follow standard practices:

1.  Fork the repository.
2.  Create a feature branch.
3.  Make your changes.
4.  Ensure code is formatted (`go fmt`) and passes `go vet` and all tests.
5.  Commit your changes and push to your branch.
6.  Create a Pull Request.

## License

This project is assumed to be under a common open-source license like MIT or Apache 2.0. Please add a `LICENSE` file if one is chosen.
