# 1. Project Goal

To create a standalone, command-line Go application that efficiently converts a directory of WebP images into a single, multi-page PDF document. Each image will occupy its own page in the PDF, and the page order will be deterministic.

# 2. Core Requirements

## Functional Requirements
- **Input:** The user must be able to specify the input directory containing `.webp` files via a command-line flag (`-i`). This should default to the current directory (`.`).
- **Output:** The user must be able to specify the name and path of the output PDF file via a command-line flag (`-o`). This should default to `output.pdf`.
- **File Filtering:** The application must automatically scan the input directory and process only files with a `.webp` extension (case-insensitively). Other files and subdirectories should be ignored.
- **Ordering:** Images must be added to the PDF in a predictable, sorted order (alphabetical by filename).
- **Page Sizing:** Each page in the PDF must be sized to match the exact dimensions of the image it contains.
- **Error Handling:** The application must handle common errors gracefully. If a single image file is corrupt or cannot be processed, it should be skipped with a warning message, and the application should continue processing the remaining files. The application should not crash.

## Non-Functional Requirements
- **Efficiency:** The application should be reasonably performant and not consume excessive memory, even with a large number of images. It should process images sequentially.
- **Portability:** Being a Go application, it should be compilable into a single, dependency-free binary for Windows, macOS, and Linux.
- **Usability:** The command-line interface should be simple and self-explanatory. The program should provide clear feedback during and after execution (e.g., "Processing file X...", "Successfully created Y.pdf").

# 3. Agent Persona & Implementation Plan

## Agent Persona: The "Go CLI Toolsmith"

An agent specializing in creating robust, efficient, and user-friendly command-line utilities in Go. The Toolsmith prioritizes clean code, clear user feedback, and reliable error handling.

## Implementation Steps

### **Step 1: Environment Setup & Dependency Management**
1.  **Initialize Go Module:** Create a new project directory and initialize a Go module to manage dependencies.
    ```bash
    mkdir webp-to-pdf
    cd webp-to-pdf
    go mod init webp-to-pdf
    ```
2.  **Identify & Install Libraries:** Identify and fetch the necessary third-party libraries.
    -   **WebP Decoding:** Go's standard library does not support WebP. The official `golang.org/x/image/webp` package is the standard choice.
    -   **PDF Creation:** The user specified a preference for `github.com/signintech/gopdf`. This library is suitable as it can handle the standard `image.Image` type.
    ```bash
    go get golang.org/x/image/webp
    go get github.com/signintech/gopdf
    ```

### **Step 2: Build the Command-Line Interface (CLI)**
1.  **Use `flag` package:** Implement the CLI using Go's standard `flag` package.
2.  **Define Flags:**
    -   Create a string flag `-i` for the input directory with a default value of `"."`.
    -   Create a string flag `-o` for the output file with a default value of `"output.pdf"`.
3.  **Parse Flags:** In the `main` function, call `flag.Parse()` to populate the variables with any user-provided values.

### **Step 3: Implement Core Logic: File Discovery & Sorting**
1.  **Read Directory:** Use `os.ReadDir` to get a list of all entries in the input directory.
2.  **Filter Files:** Iterate through the directory entries.
    -   Check if the entry is a file (not a directory).
    -   Check if the filename has a `.webp` suffix (using `strings.HasSuffix` and `strings.ToLower` for case-insensitivity).
    -   Append the names of valid WebP files to a string slice.
3.  **Handle No-Files Case:** If the slice of filenames is empty after filtering, print an informative error message and exit gracefully.
4.  **Sort Files:** Use `sort.Strings` to sort the slice of filenames alphabetically. This ensures consistent output.

### **Step 4: Implement Core Logic: PDF Generation & Image Processing**
1.  **Initialize PDF:** Create a new `gopdf.GoPdf` object and start it with a default configuration (`pdf.Start`).
2.  **Loop Through Files:** Iterate over the sorted slice of filenames. For each filename:
    a. **Log Progress:** Print a message indicating which file is being processed.
    b. **Open File:** Open the image file. Handle potential errors.
    c. **Decode WebP:** Use `webp.Decode()` to decode the file's content into an `image.Image` object. This is a critical step and must be wrapped in error handling.
    d. **Get Dimensions:** Get the image's width and height from `img.Bounds().Dx()` and `img.Bounds().Dy()`.
    e. **Add Page to PDF:** Use `pdf.AddPageWithOption()` to create a new page in the PDF, setting the `PageSize` to the exact dimensions of the image.
    f. **Create Image Holder:** Convert the `image.Image` object into a `gopdf.ImageHolder` using the `gopdf.ImageHolderByImage()` function. This is the crucial step for this specific library.
    g. **Draw Image:** Use `pdf.ImageByHolder()` to draw the image onto the new page, placing it at coordinates (0,0) and making it fill the page.
    h. **Robust Loop:** Wrap the image processing steps (open, decode, draw) in error checks. If any step fails, log a warning message with the filename and error, then `continue` to the next file.
3.  **Finalize PDF:** After the loop finishes, call `pdf.WritePdf()` to save the complete PDF to the output file path.
4.  **Provide Feedback:** Print a final success message to the user confirming the PDF creation.

# 4. Code Structure (`main.go`)

```go
package main

import (
    // Necessary standard library and third-party imports
)

// main() function:
// - Initializes and parses command-line flags (-i, -o).
// - Calls the primary conversion function.
// - Handles the final success or fatal error message.
func main() {
    // ... flag definitions and parsing ...
    // ... call to convertWebPToPDF ...
    // ... print success/failure ...
}

// convertWebPToPDF(inputDir string, outputFile string) error function:
// - Contains all the core logic.
// - Returns an error to the caller (main).
// - Step 1: Reads and filters .webp files from inputDir.
// - Step 2: Sorts the file list.
// - Step 3: Initializes the GoPdf object.
// - Step 4: Loops through files:
//     - Opens, decodes, and gets dimensions.
//     - Adds a correctly sized page.
//     - Creates an ImageHolder and draws the image.
//     - Skips and logs errors for individual files.
// - Step 5: Writes the final PDF to outputFile.
func convertWebPToPDF(inputDir, outputFile string) error {
    // ... implementation ...
}