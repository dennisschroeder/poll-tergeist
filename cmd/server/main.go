// Command server runs poll-tergeist: one binary embedding its migrations
// and static views, wired to Postgres and an in-process SSE hub.
package main

import (
	"context"
	"errors"
	"log"
	"net/http"
	"os"
	"os/signal"
	"syscall"
	"time"

	apphttp "github.com/dennisschroeder/poll-tergeist/internal/http"
	"github.com/dennisschroeder/poll-tergeist/internal/live"
	"github.com/dennisschroeder/poll-tergeist/internal/store"
	"github.com/dennisschroeder/poll-tergeist/web"
)

func main() {
	if err := run(); err != nil {
		log.Fatal(err)
	}
}

func run() error {
	addr := envOr("ADDR", ":8080")
	dbURL := envOr("DATABASE_URL", "postgres://poll:poll@localhost:5432/poll_tergeist?sslmode=disable")

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	s, err := store.Open(ctx, dbURL)
	if err != nil {
		return err
	}
	defer s.Close()

	hub := live.NewHub()
	handler := apphttp.NewRouter(s, hub, web.FS)

	srv := &http.Server{
		Addr:              addr,
		Handler:           handler,
		ReadHeaderTimeout: 5 * time.Second,
	}

	errCh := make(chan error, 1)
	go func() {
		log.Printf("poll-tergeist listening on %s", addr)
		if err := srv.ListenAndServe(); err != nil && !errors.Is(err, http.ErrServerClosed) {
			errCh <- err
			return
		}
		errCh <- nil
	}()

	select {
	case err := <-errCh:
		return err
	case <-ctx.Done():
	}

	shutdownCtx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	log.Print("shutting down")
	return srv.Shutdown(shutdownCtx)
}

func envOr(key, fallback string) string {
	if v := os.Getenv(key); v != "" {
		return v
	}
	return fallback
}
