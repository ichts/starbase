// Package app wires the application together: catalog, database, writer
// and HTTP handler. Used by the server binary and by tools that need the
// real app in-process (cmd/manifests).
package app

import (
	"context"
	"log/slog"
	"net/http"
	"path/filepath"
	"time"

	"github.com/zweiundeins/lith/writer"

	"starbase/components"
	"starbase/content"
	"starbase/internal/catalog"
	"starbase/internal/commands"
	"starbase/internal/config"
	"starbase/internal/cqrs"
	"starbase/internal/db"
	"starbase/internal/queries"
	"starbase/internal/tscheck"
	"starbase/internal/web"
	"starbase/static"
)

type App struct {
	Handler http.Handler
	Catalog *catalog.Catalog
	w       *writer.Writer
	close   func()
}

// Close applies the commands still queued, stops the writer and closes the
// database. Call it after the HTTP server has shut down.
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
	hub := cqrs.NewHub()
	w, err := writer.New(database, writer.Options{OnCommit: hub.Changed, Log: log})
	if err != nil {
		database.Close()
		return nil, err
	}
	// The writer outlives the HTTP server, so in-flight commands can finish:
	// Close applies what is queued.
	go w.Run(context.Background())
	closeAll := func() {
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
