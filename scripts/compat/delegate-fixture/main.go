package main

import (
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"net"
	"net/http"
	"os"
	"os/signal"
	"path/filepath"
	"strings"
	"syscall"
	"time"

	workspace "synon-go/internal/persistence/workspace"
	"synon-go/internal/server"
	delegatefixture "synon-go/internal/testsupport/delegatefixture"
)

type readyReport struct {
	Status    string                  `json:"status"`
	BaseURL   string                  `json:"baseUrl"`
	Database  string                  `json:"database"`
	Temporary bool                    `json:"temporary"`
	Fixture   delegatefixture.Fixture `json:"fixture"`
}

func main() {
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	if err := run(ctx, os.Args[1:], os.Stdout); err != nil {
		fmt.Fprintln(os.Stderr, "delegate fixture:", err)
		os.Exit(1)
	}
}

func run(ctx context.Context, args []string, output io.Writer) error {
	flags := flag.NewFlagSet("delegate-fixture", flag.ContinueOnError)
	listenAddress := flags.String("listen", "127.0.0.1:0", "loopback listen address")
	databasePath := flags.String("database", "", "optional persistent test database path")
	if err := flags.Parse(args); err != nil {
		return err
	}
	if flags.NArg() != 0 {
		return errors.New("unexpected positional arguments")
	}
	if err := validateLoopbackListen(*listenAddress); err != nil {
		return err
	}

	temporary := strings.TrimSpace(*databasePath) == ""
	var temporaryRoot string
	if temporary {
		var err error
		temporaryRoot, err = os.MkdirTemp("", "synon-delegate-fixture-*")
		if err != nil {
			return fmt.Errorf("create temporary fixture root: %w", err)
		}
		defer os.RemoveAll(temporaryRoot)
		*databasePath = filepath.Join(temporaryRoot, "workspace.db")
	} else {
		absolute, err := filepath.Abs(strings.TrimSpace(*databasePath))
		if err != nil {
			return fmt.Errorf("resolve database path: %w", err)
		}
		*databasePath = absolute
	}

	store, err := workspace.Open(*databasePath)
	if err != nil {
		return err
	}
	defer store.Close()
	fixture, err := delegatefixture.Seed(store)
	if err != nil {
		return err
	}
	listener, err := net.Listen("tcp", *listenAddress)
	if err != nil {
		return fmt.Errorf("listen: %w", err)
	}
	defer listener.Close()
	productionHandler := server.New(server.Options{Workspace: store}).Handler()
	testHandler := http.HandlerFunc(func(response http.ResponseWriter, request *http.Request) {
		if strings.TrimSpace(request.Header.Get("X-Synon-User-Id")) == "" {
			request.Header.Set("X-Synon-User-Id", delegatefixture.UserID)
		}
		productionHandler.ServeHTTP(response, request)
	})
	httpServer := &http.Server{
		Handler:           testHandler,
		ReadHeaderTimeout: 5 * time.Second,
	}
	baseURL := "http://" + listener.Addr().String()
	if err := json.NewEncoder(output).Encode(readyReport{
		Status: "ready", BaseURL: baseURL, Database: *databasePath,
		Temporary: temporary, Fixture: fixture,
	}); err != nil {
		return fmt.Errorf("write readiness report: %w", err)
	}

	serveResult := make(chan error, 1)
	go func() { serveResult <- httpServer.Serve(listener) }()
	select {
	case err := <-serveResult:
		if !errors.Is(err, http.ErrServerClosed) {
			return err
		}
		return nil
	case <-ctx.Done():
		shutdownCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		if err := httpServer.Shutdown(shutdownCtx); err != nil {
			return fmt.Errorf("shutdown: %w", err)
		}
		return nil
	}
}

func validateLoopbackListen(address string) error {
	host, _, err := net.SplitHostPort(strings.TrimSpace(address))
	if err != nil {
		return fmt.Errorf("invalid --listen address: %w", err)
	}
	ip := net.ParseIP(strings.Trim(host, "[]"))
	if !strings.EqualFold(host, "localhost") && (ip == nil || !ip.IsLoopback()) {
		return errors.New("--listen must use a loopback host")
	}
	return nil
}
