// Package app wires the application together: catalog, database, writer
// and HTTP handler. Used by the server binary and by tools that need the
// real app in-process (cmd/manifests).
package app

import (
	"context"
	"database/sql"
	"log/slog"
	"net/http"
	"path/filepath"
	"time"

	"github.com/zweiundeins/lith/live"
	"github.com/zweiundeins/lith/sse"
	"github.com/zweiundeins/lith/writer"

	"starbase/components"
	"starbase/content"
	"starbase/internal/catalog"
	"starbase/internal/commands"
	"starbase/internal/config"
	"starbase/internal/db"
	"starbase/internal/queries"
	"starbase/internal/tscheck"
	"starbase/internal/web"
	"starbase/static"
)

// Memory for deploy/starbase.service's MemoryMax=384M, measured on
// 2026-10-13 with the production binary: 105 MiB idle after warm-up, plus
// 1.1 MiB of heap per open stream on /showcase to 1.36 MiB on
// /components/data-table (the largest frame) with zstd's low-memory
// history, 1.7 to 1.95 MiB without it. 105 + 150 × 1.36 = 309 MiB, 80% of
// MemoryMax; the rest is for the type check's compiler (about 60 MB).
const maxStreams = 150

// MemoryLimit is the GC's soft limit, which main sets unless GOMEMLIMIT is
// set. Without it the heap grows to twice what is live: 150 streams took
// 449 to 494 MiB while the board was painted, 247 to 263 MiB with it.
const MemoryLimit = 256 << 20

// HubOptions configure the render loop.
func HubOptions(read *sql.DB, log *slog.Logger) live.Options {
	return live.Options{Read: read, Log: log, MaxStreams: maxStreams, SSE: sse.Options{ZstdLowMemory: true}}
}

type App struct {
	Handler http.Handler
	Catalog *catalog.Catalog
	w       *writer.Writer
	close   func()
}

// Close ends the render streams, applies the commands still queued, stops
// the writer and closes the database. Call it after the HTTP server has shut
// down.
func (a *App) Close() { a.close() }

// Settle returns once the commands sent before it, such as the startup
// seeding, are applied.
func (a *App) Settle(ctx context.Context) error { return a.w.Flush(ctx) }

// New builds the app. ctx bounds the lifetime of render streams: cancel it
// to end them (e.g. before http.Server.Shutdown).
func New(ctx context.Context, cfg config.Config, log *slog.Logger) (*App, error) {
	cat, err := catalog.Load(components.FS)
	if err != nil {
		return nil, err
	}
	database, err := db.Open(ctx, cfg.DBPath)
	if err != nil {
		return nil, err
	}
	hub := live.New(HubOptions(database.R, log))
	w, err := writer.New(database, writer.Options{OnCommit: hub.Changed, Log: log})
	if err != nil {
		database.Close()
		return nil, err
	}
	// The writer outlives the HTTP server, so in-flight commands can finish:
	// Close applies what is queued.
	go w.Run(context.Background())
	hubCtx, stopHub := context.WithCancel(ctx)
	hubDone := make(chan struct{})
	go func() {
		defer close(hubDone)
		if err := hub.Run(hubCtx); err != nil {
			log.Error("render loop", "err", err)
		}
	}()
	closeAll := func() {
		stopHub()
		<-hubDone // its workers give their read connections back
		if err := w.Close(); err != nil {
			log.Warn("closing the writer", "err", err)
		}
		database.Close()
	}

	if err := w.Do(ctx, commands.SyncCatalog{Catalog: cat, Datastar: static.Datastar()}); err != nil {
		closeAll()
		return nil, err
	}
	w.Send(commands.PruneTabs{OlderThan: 30 * 24 * time.Hour})
	w.Send(commands.PruneSessionPrefs{OlderThan: 400 * 24 * time.Hour})
	w.Send(commands.SeedBoard{})
	w.Send(commands.SeedDemo{})
	w.Send(commands.SeedStars{})
	log.Info("catalog synced", "components", len(cat.Components), "hash", cat.Hash)

	// The type check's compiler unpacks next to the database on the first check.
	checker := tscheck.New(filepath.Dir(cfg.DBPath))
	srv := web.New(ctx, web.Deps{
		Config:   cfg,
		Log:      log,
		Writer:   w,
		Hub:      hub,
		Queries:  queries.New(database),
		Catalog:  cat,
		StaticFS: static.FS,
		Content:  content.FS,
		Checker:  checker,
	})
	return &App{Handler: srv.Handler(), Catalog: cat, w: w, close: closeAll}, nil
}
