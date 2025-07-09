## Agent Instructions for Manga/Image to PDF Converter API Development

This document provides guidelines for AI agents working on this project. The primary goal is to develop a robust, performant, and easy-to-use API for the existing image-to-PDF conversion functionality.

### 1. Versioning

*   **API Versioning**: The API will follow Semantic Versioning 2.0.0 (SemVer). Version numbers will be in the format MAJOR.MINOR.PATCH.
    *   Increment MAJOR version for incompatible API changes.
    *   Increment MINOR version for adding functionality in a backward-compatible manner.
    *   Increment PATCH version for backward-compatible bug fixes.
*   **Code Versioning**: Use Git for version control. Branch names should be descriptive (e.g., `feature/add-tiff-support`, `fix/pdf-generation-bug`). Commit messages should follow conventional commit formats.

### 2. Dependency Management

*   **Go Modules**: This project uses Go Modules for dependency management. The `go.mod` file lists the direct dependencies and their versions.
*   **Current Key Dependencies**:
    *   `github.com/disintegration/imaging v1.6.2`: Used for image processing.
    *   `github.com/jung-kurt/gofpdf v0.0.0-20191119144553-603f56990463`: Used for PDF generation.
    *   `golang.org/x/image v0.28.0`: Used for decoding various image formats.
*   **Updating Dependencies**: Before adding or updating dependencies, evaluate their stability, performance, and licensing. Update `go.mod` using `go get` and ensure all tests pass.

### 3. API Design Principles

*   **Simplicity**:
    *   Endpoints should be intuitive and easy to understand.
    *   Request and response payloads should be minimal and clear.
    *   Avoid unnecessary complexity in API logic.
*   **Performance**:
    *   Prioritize efficient image processing and PDF generation.
    *   Utilize concurrency and parallelism where appropriate (as already done in the CLI tool).
    *   Optimize memory usage, especially when handling large images or many files.
    *   Consider asynchronous processing for long-running conversion tasks, potentially using a job queue system if the API needs to scale.
*   **Statelessness**: Design API endpoints to be stateless. Each request from a client should contain all the information needed to understand and process the request.
*   **Clear Error Handling**:
    *   Use standard HTTP status codes to indicate success or failure.
    *   Provide meaningful error messages in response bodies (e.g., JSON format) to help clients diagnose issues.
*   **Consistency**: Maintain consistency in naming conventions, request/response structures, and error handling across all endpoints.
*   **Security**:
    *   Implement appropriate security measures (e.g., input validation, rate limiting if necessary).
    *   Authentication/Authorization: Initially, the API might be simple without auth, but design with future auth needs in mind (e.g., API keys, OAuth2). This needs further discussion based on deployment context.

### 4. Performance Guidelines

*   **Benchmarking**: Implement benchmarks for critical code paths, especially image decoding, processing, and PDF writing.
*   **Profiling**: Regularly use Go's profiling tools (`pprof`) to identify and address CPU and memory bottlenecks. The existing CLI flags for profiling (`-cpuprofile`, `-memprofile`) should be maintained and potentially adapted for API profiling.
*   **Resource Management**: Ensure proper handling of resources like file descriptors and memory to prevent leaks.
*   **Concurrency**: Leverage Go's concurrency features (goroutines, channels) effectively, but be mindful of potential race conditions and deadlocks. Test concurrent code thoroughly.
*   **Image Optimization**: If possible and within scope, consider options for image pre-processing or optimization before PDF conversion to reduce file sizes and improve processing speed, without significant quality loss.

### 5. Simplicity Goals

*   **Code Readability**: Write clean, well-documented Go code. Follow standard Go formatting (`gofmt`).
*   **Maintainability**: Structure the codebase logically. Separate concerns (e.g., API handling, image processing, PDF generation) into distinct packages or modules.
*   **Ease of Use (API)**:
    *   The API should be straightforward for client developers to integrate with.
    *   Provide clear documentation (e.g., OpenAPI/Swagger specification).
*   **Minimal Configuration**: Aim for sensible defaults and minimize the need for complex configuration.

### 6. Development Workflow

1.  **Understand Requirements**: Ensure clarity on the features or bug fixes before starting implementation.
2.  **Design (if applicable)**: For new features, briefly outline the API endpoints, request/response formats, and internal logic.
3.  **Implement**: Write code following the guidelines in this document.
4.  **Test**:
    *   Write unit tests for new logic.
    *   Write integration tests for API endpoints.
    *   Ensure all tests pass.
5.  **Document**: Update API documentation and any relevant parts of `README.md`.
6.  **Review**: If working in a team, changes should be peer-reviewed.

### 7. Testing

*   **Unit Tests**: Cover individual functions and packages.
*   **Integration Tests**: Test the interaction between different components, especially the API endpoints and the core conversion logic.
*   **End-to-End (E2E) Tests (Future)**: As the API matures, E2E tests simulating client interactions would be beneficial.
*   **Performance Tests**: Use benchmarks to ensure performance targets are met.

By adhering to these guidelines, we aim to create a high-quality, maintainable, and efficient API.
