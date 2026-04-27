package main

import (
	"context"
	"log/slog"
	"net/http"
	"os"
	"os/signal"
	"syscall"
	"time"
)

type cloudLoggingHandler struct {
	handler slog.Handler
}

func (h *cloudLoggingHandler) Enabled(ctx context.Context, level slog.Level) bool {
	return h.handler.Enabled(ctx, level)
}

func (h *cloudLoggingHandler) Handle(ctx context.Context, r slog.Record) error {
	var severity string
	switch {
	case r.Level >= slog.LevelError:
		severity = "ERROR"
	case r.Level >= slog.LevelWarn:
		severity = "WARNING"
	case r.Level >= slog.LevelInfo:
		severity = "INFO"
	default:
		severity = "DEFAULT"
	}
	r.AddAttrs(slog.String("severity", severity))
	return h.handler.Handle(ctx, r)
}

func (h *cloudLoggingHandler) WithAttrs(attrs []slog.Attr) slog.Handler {
	return &cloudLoggingHandler{handler: h.handler.WithAttrs(attrs)}
}

func (h *cloudLoggingHandler) WithGroup(name string) slog.Handler {
	return &cloudLoggingHandler{handler: h.handler.WithGroup(name)}
}

func main() {
	jsonHandler := slog.NewJSONHandler(os.Stdout, &slog.HandlerOptions{Level: slog.LevelDebug})
	logger := slog.New(&cloudLoggingHandler{handler: jsonHandler})
	slog.SetDefault(logger)

	port := os.Getenv("PORT")
	if port == "" {
		port = "8080"
	}

	var (
		storage Storage
		err     error
	)

	if os.Getenv("STORAGE_BACKEND") == "local" {
		storagePath := os.Getenv("STORAGE_PATH")
		if storagePath == "" {
			storagePath = "./storage"
		}
		storage = NewLocalStorage(storagePath)
	} else {
		storage, err = NewGCSStorage("github-package-registry-storage")
		if err != nil {
			logger.Error("failed to create GCS storage", "error", err)
			os.Exit(1)
		}
	}

	app := NewDefaultApp(storage, logger)
	mux := http.NewServeMux()

	mux.HandleFunc("GET /healthz", func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte("OK"))
	})

	mux.HandleFunc("GET /favicon.ico", func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusNotFound)
	})

	const indexHTML = `<!DOCTYPE html>
<html>
<head>
    <meta charset="UTF-8">
    <title>Github Package Registry mirror</title>
</head>
<body>
<h1>Mirror for GitHub Package Registry</h1>

<a href="https://github.com/navikt/utvikling/blob/main/docs/teknisk/Konsumere%20biblioteker%20fra%20Github%20Package%20Registry.md">Read the documentation.</a>

</body>
</html>`

	mux.HandleFunc("GET /simple/{repo}/{path...}", func(w http.ResponseWriter, r *http.Request) {
		app.handleSimple(w, r, r.PathValue("repo"), r.PathValue("path"))
	})

	mux.HandleFunc("GET /cached/{repo}/{path...}", func(w http.ResponseWriter, r *http.Request) {
		app.handleCached(w, r, r.PathValue("repo"), r.PathValue("path"))
	})

	mux.HandleFunc("GET /{repo}/{path...}", func(w http.ResponseWriter, r *http.Request) {
		app.handleSimple(w, r, r.PathValue("repo"), r.PathValue("path"))
	})

	mux.HandleFunc("GET /", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		_, _ = w.Write([]byte(indexHTML))
	})

	server := &http.Server{
		Addr:              ":" + port,
		Handler:           mux,
		ReadHeaderTimeout: 10 * time.Second,
		ReadTimeout:       30 * time.Second,
		WriteTimeout:      10 * time.Minute,
		IdleTimeout:       120 * time.Second,
		MaxHeaderBytes:    1 << 20,
	}

	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGTERM, syscall.SIGINT)
	defer stop()

	logger.Info("server starting", "port", port)
	go func() {
		if err := server.ListenAndServe(); err != nil && err != http.ErrServerClosed {
			logger.Error("server error", "error", err)
		}
	}()

	<-ctx.Done()
	logger.Info("shutting down server")

	shutdownCtx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	if err := server.Shutdown(shutdownCtx); err != nil {
		logger.Error("shutdown error", "error", err)
	}
}
