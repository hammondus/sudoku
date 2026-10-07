// Command sudoku serves the Sudoku PWA: the static app, puzzle generation,
// hints, email-code sign-in, and sync of games between a player's devices.
//
// Run it as a server, or with -invite, -revoke, or -users to manage the
// accounts that may sign in.
package main

import (
	"context"
	"embed"
	"flag"
	"fmt"
	"html/template"
	"io/fs"
	"log"
	"log/slog"
	"net/http"
	"os"
	"runtime"
	"strings"
	"time"

	"github.com/hammondus/mailer"
	"github.com/hammondus/nitrokit"
)

//go:embed all:web
var embedded embed.FS

// VERSION is the one place the app's version lives. The server reports it to
// the browser through /js/version.js, the service worker names its cache
// after it, and the client compares it to spot a new release.
//
//go:embed VERSION
var versionFile string

var appVersion = strings.TrimSpace(versionFile)

// csp is nitrokit's default plus connect-src for the app's own API calls,
// and the manifest. Everything is same-origin; there is no third-party code.
const csp = "default-src 'none'; style-src 'self'; script-src 'self'; img-src 'self' data:; " +
	"connect-src 'self'; manifest-src 'self'; worker-src 'self'; " +
	"base-uri 'none'; form-action 'self'; frame-ancestors 'none'"

func main() {
	addr := flag.String("addr", ":8080", "listen address")
	dbPath := flag.String("db", nitrokit.EnvOr("SUDOKU_DB", "sudoku.db"), "SQLite database path (env SUDOKU_DB)")
	dev := flag.Bool("dev", false, "serve web/ from disk, allow the session cookie over plain HTTP")
	healthcheck := flag.Bool("healthcheck", false, "probe a running server's /healthz on -addr and exit")
	invite := flag.String("invite", "", "create an account for this email address, then exit")
	revoke := flag.String("revoke", "", "delete this account and all its games, then exit")
	users := flag.Bool("users", false, "list accounts, then exit")
	flag.Parse()

	// The image is distroless: no shell, no curl. The container health check
	// runs this binary with -healthcheck instead.
	if *healthcheck {
		if err := nitrokit.HealthProbe(*addr); err != nil {
			log.Fatal(err)
		}
		return
	}

	st, err := openStore(*dbPath)
	if err != nil {
		log.Fatalf("open database %s: %v", *dbPath, err)
	}
	defer st.close()

	if *invite != "" || *revoke != "" || *users {
		if err := manageAccounts(st, *invite, *revoke, *users); err != nil {
			log.Fatal(err)
		}
		return
	}

	if err := serve(st, *addr, *dev); err != nil {
		log.Fatal(err)
	}
}

func manageAccounts(st *store, invite, revoke string, list bool) error {
	ctx := context.Background()
	if invite != "" {
		if err := st.invite(ctx, normEmail(invite)); err != nil {
			return err
		}
		fmt.Printf("invited %s\n", normEmail(invite))
	}
	if revoke != "" {
		ok, err := st.revoke(ctx, normEmail(revoke))
		if err != nil {
			return err
		}
		if !ok {
			return fmt.Errorf("no account for %s", normEmail(revoke))
		}
		fmt.Printf("deleted %s and its games\n", normEmail(revoke))
	}
	if list {
		emails, err := st.listUsers(ctx)
		if err != nil {
			return err
		}
		for _, e := range emails {
			fmt.Println(e)
		}
	}
	return nil
}

// assetTree is the part of nitrokit.Assets and nitrokit.DirAssets that the
// server uses, so -dev can swap one for the other.
type assetTree interface {
	http.Handler
	URL(name string) string
}

func serve(st *store, addr string, dev bool) error {
	logger := slog.New(slog.NewTextHandler(os.Stderr, nil))

	smtp := mailer.ConfigFromEnv("SUDOKU_SMTP_")
	var sender mailer.Sender
	if smtp.Configured() {
		sender = mailer.NewSMTP(smtp)
		logger.Info("sign-in email: sending via SMTP", "host", smtp.Host, "from", smtp.From)
	} else {
		sender = mailer.NewLog(logger)
		logger.Warn("sign-in email: SMTP not configured (set SUDOKU_SMTP_HOST and SUDOKU_SMTP_FROM); codes are logged, not sent")
	}

	handler, err := newHandler(st, sender, logger, dev)
	if err != nil {
		return err
	}

	go func() {
		for range time.Tick(time.Hour) {
			if err := st.sweep(context.Background()); err != nil {
				logger.Error("sweep", "err", err)
			}
		}
	}()

	logger.Info("listening", "addr", addr, "dev", dev, "version", appVersion)
	return nitrokit.Run(context.Background(), nitrokit.NewServer(addr, nitrokit.AccessLog(logger, handler)))
}

// newHandler builds every route. It is separate from serve so tests can
// drive the real handler with httptest.
func newHandler(st *store, sender mailer.Sender, logger *slog.Logger, dev bool) (http.Handler, error) {
	var webRoot fs.FS
	var assets assetTree
	var err error
	if dev {
		webRoot = os.DirFS("web")
		assets, err = nitrokit.NewDirAssets("web", "/")
	} else {
		if webRoot, err = fs.Sub(embedded, "web"); err != nil {
			return nil, err
		}
		assets, err = nitrokit.NewAssets(webRoot, "/")
	}
	if err != nil {
		return nil, fmt.Errorf("assets: %w", err)
	}

	// The shell is the only template. It exists to stamp content-hashed
	// asset URLs, so a deploy that changes a file changes its URL.
	parseShell := func() (*nitrokit.Templates, error) {
		return nitrokit.ParseTemplates(webRoot, template.FuncMap{"asset": assets.URL})
	}
	shell, err := parseShell()
	if err != nil {
		return nil, fmt.Errorf("parse shell: %w", err)
	}

	// Behind Nginx Proxy Manager on a Docker network with no fixed address,
	// so any private peer is the proxy. A public peer's X-Forwarded-For is
	// ignored.
	trust := nitrokit.TrustPrivateProxies()
	au := newAuth(st, sender, logger, trust, !dev)
	ap := newAPI(st, logger, trust, max(1, runtime.NumCPU()))

	mux := http.NewServeMux()
	mux.HandleFunc("GET /healthz", nitrokit.Healthz)

	mux.HandleFunc("POST /api/auth/code", au.handleCode)
	mux.HandleFunc("POST /api/auth/verify", au.handleVerify)
	mux.HandleFunc("POST /api/auth/logout", au.handleLogout)
	mux.HandleFunc("GET /api/me", au.user(au.handleMe))

	mux.HandleFunc("GET /api/puzzle", ap.handlePuzzle)
	mux.HandleFunc("POST /api/hint", ap.handleHint)
	mux.HandleFunc("GET /api/sync", au.user(ap.handleSync))
	mux.HandleFunc("PUT /api/games/{id}", au.user(ap.handlePutGame))
	mux.HandleFunc("PUT /api/settings", au.user(ap.handlePutSettings))

	// Generated rather than a file: it can't go stale against VERSION.
	// no-cache, because the service worker's cache name derives from it.
	mux.HandleFunc("GET /js/version.js", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/javascript; charset=utf-8")
		nitrokit.NoCache(w)
		fmt.Fprintf(w, "export const APP_VERSION = %q;\n", appVersion)
	})

	// Every other path is the shell or a file from web/. nitrokit.Assets
	// serves a file immutable for a year when its ?v= hash matches, and
	// no-cache with an ETag otherwise. The unversioned case covers the ES
	// modules app.js imports by plain URL, sw.js (fetched by a fixed URL),
	// and the manifest and its icons.
	mux.HandleFunc("GET /", func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/" && r.URL.Path != "/index.html" {
			assets.ServeHTTP(w, r)
			return
		}
		t := shell
		if dev {
			var err error
			if t, err = parseShell(); err != nil {
				logger.Error("parse shell", "err", err)
				http.Error(w, "template error", http.StatusInternalServerError)
				return
			}
		}
		// Render sets no-cache and an ETag over the rendered bytes.
		if err := t.Render(w, r, "index.html", http.StatusOK, nil); err != nil {
			logger.Error("render shell", "err", err)
		}
	})

	return nitrokit.SecureHeaders(csp, "", mux), nil
}
