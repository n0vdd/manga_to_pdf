package main

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"os"
	"os/signal"
	"syscall" // For SIGTERM
	"time"

	"manga_to_pdf/api" // Import the new api package
	// "manga_to_pdf/internal/converter" // No longer directly needed by main
)

// Config holds all application configuration for the server.
type Config struct {
	ListenAddress  string
	VerboseLogging bool
	// CPUProfileFile string // Profiling can be added back if needed via HTTP endpoints (e.g. net/http/pprof)
	// MemProfileFile string
}

func main() {
	cfg := Config{
		ListenAddress:  ":8080", // Default listen address
		VerboseLogging: false,   // Default logging level
	}

	// Basic environment variable configuration (optional)
	if addr := os.Getenv("LISTEN_ADDRESS"); addr != "" {
		cfg.ListenAddress = addr
	}
	if verbose := os.Getenv("VERBOSE_LOGGING"); verbose == "true" || verbose == "1" {
		cfg.VerboseLogging = true
	}

	// Setup structured logger
	var logLevel slog.Level
	if cfg.VerboseLogging {
		logLevel = slog.LevelDebug
	} else {
		logLevel = slog.LevelInfo
	}
	logger := slog.New(slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{Level: logLevel}))
	slog.SetDefault(logger)

	slog.Info("Starting API server...", "address", cfg.ListenAddress, "verbose_logging", cfg.VerboseLogging)

	// Setup HTTP server and router
	mux := http.NewServeMux()
	mux.HandleFunc("/convert", api.HandleConvert) // Register the /convert handler

	// Add health check endpoint
	mux.HandleFunc("/health", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusOK)
		fmt.Fprintln(w, `{"status":"ok"}`)
	})

	// Consider adding pprof endpoints for profiling if needed
	// mux.HandleFunc("/debug/pprof/", pprof.Index)
	// mux.HandleFunc("/debug/pprof/cmdline", pprof.Cmdline)
	// mux.HandleFunc("/debug/pprof/profile", pprof.Profile)
	// mux.HandleFunc("/debug/pprof/symbol", pprof.Symbol)
	// mux.HandleFunc("/debug/pprof/trace", pprof.Trace)


	server := &http.Server{
		Addr:    cfg.ListenAddress,
		Handler: mux,
		// ReadTimeout:  5 * time.Second, // Example: Add timeouts for security
		// WriteTimeout: 60 * time.Second, // Example: Longer for PDF generation
		// IdleTimeout:  120 * time.Second,
	}

	// Graceful shutdown
	idleConnsClosed := make(chan struct{})
	go func() {
		sigChan := make(chan os.Signal, 1)
		signal.Notify(sigChan, os.Interrupt, syscall.SIGTERM)
		sig := <-sigChan
		slog.Info("Received signal, shutting down gracefully...", "signal", sig)

		shutdownCtx, cancel := context.WithTimeout(context.Background(), 30*time.Second) // 30-second shutdown timeout
		defer cancel()

		if err := server.Shutdown(shutdownCtx); err != nil {
			slog.Error("HTTP server Shutdown error", "error", err)
		}
		slog.Info("HTTP server shutdown complete.")
		close(idleConnsClosed)
	}()

	slog.Info("Server is listening", "address", cfg.ListenAddress)
	if err := server.ListenAndServe(); err != nil && !errors.Is(err, http.ErrServerClosed) {
		slog.Error("Failed to start HTTP server", "error", err)
		os.Exit(1)
	}

	<-idleConnsClosed // Wait for graceful shutdown to complete
	slog.Info("Application shut down successfully.")
}
