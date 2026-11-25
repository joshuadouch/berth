// Command berth is a small hosting control panel for WordPress and PHP sites
// on nginx, PHP-FPM and MariaDB.
package main

import (
	"bufio"
	"context"
	"crypto/tls"
	"errors"
	"flag"
	"fmt"
	"log/slog"
	"net/http"
	"os"
	"os/signal"
	"strings"
	"syscall"
	"time"

	"github.com/joshuadouch/berth/internal/api"
	"github.com/joshuadouch/berth/internal/auth"
	"github.com/joshuadouch/berth/internal/config"
	"github.com/joshuadouch/berth/internal/engine"
	"github.com/joshuadouch/berth/internal/plugin"
	"github.com/joshuadouch/berth/internal/plugins"
	"github.com/joshuadouch/berth/internal/render"
	"github.com/joshuadouch/berth/internal/store"
	"github.com/joshuadouch/berth/internal/web"
	"golang.org/x/term"
)

// version is set at build time with -ldflags "-X main.version=...".
var version = "dev"

func usage() {
	fmt.Fprintf(os.Stderr, `Berth %s

Usage:
  berth serve   [-config /etc/berth/berth.yaml]   run the API, UI and scheduled jobs
  berth apply   [-config ...]                     write nginx and PHP-FPM config for every site
  berth render  [-config ...]                     print the config without writing it
  berth passwd  [-config ...]                     reset the admin password and turn off 2FA
  berth version
`, version)
}

func main() {
	if len(os.Args) < 2 {
		usage()
		os.Exit(2)
	}
	cmd, args := os.Args[1], os.Args[2:]
	fs := flag.NewFlagSet(cmd, flag.ExitOnError)
	cfgPath := fs.String("config", "/etc/berth/berth.yaml", "config file")
	_ = fs.Parse(args)
	var err error
	switch cmd {
	case "serve":
		err = serve(*cfgPath)
	case "apply":
		err = applyCmd(*cfgPath)
	case "render":
		err = renderCmd(*cfgPath)
	case "passwd":
		err = passwd(*cfgPath)
	case "version", "-v", "--version":
		fmt.Println(version)
	default:
		usage()
		os.Exit(2)
	}
	if err != nil {
		fmt.Fprintln(os.Stderr, "error:", err)
		os.Exit(1)
	}
}

func open(cfgPath string) (*config.Config, *store.Store, error) {
	cfg, err := config.Load(cfgPath)
	if err != nil {
		return nil, nil, err
	}
	if err := os.MkdirAll(cfg.DataDir, 0o750); err != nil {
		return nil, nil, err
	}
	st, err := store.Open(cfg.Path("berth.db"))
	if err != nil {
		return cfg, nil, err
	}
	// Panels from before accounts existed get one account that owns everything.
	if err := st.MigrateAccounts(context.Background()); err != nil {
		st.Close()
		return cfg, nil, fmt.Errorf("migrate accounts: %w", err)
	}
	return cfg, st, nil
}

func serve(cfgPath string) error {
	log := slog.New(slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{Level: slog.LevelInfo}))
	cfg, st, err := open(cfgPath)
	if err != nil {
		return err
	}
	defer st.Close()
	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer stop()

	eng := engine.New(cfg, st, log)
	eng.Version = version
	reg := plugin.NewRegistry(st, log, plugins.All()...)
	eng.Plugins = reg
	eng.Hooks = reg
	eng.Challenges = reg
	// Halyard and Cloudflare were settings before they were plugins.
	if err := plugins.Migrate(ctx, st, reg); err != nil {
		log.Error("moving Halyard and Cloudflare settings into plugins failed", "err", err)
	}
	// Re-apply on start so fixes in a new release reach every site.
	if err := eng.Apply(ctx); err != nil {
		log.Error("startup apply failed; existing config left in place", "err", err)
	}
	go eng.Loop(ctx)

	certFile, keyFile := cfg.TLSCert, cfg.TLSKey
	if certFile == "" {
		certFile, keyFile = cfg.Path("ui-cert.pem"), cfg.Path("ui-key.pem")
		if err := auth.EnsureSelfSigned(certFile, keyFile, cfg.Name); err != nil {
			return err
		}
	}
	srv := &api.Server{Cfg: cfg, Store: st, Engine: eng, Log: log, UI: web.FS(), Version: version, Plugins: reg}
	hs := &http.Server{
		Addr:              cfg.Listen,
		Handler:           srv.Handler(),
		ReadHeaderTimeout: 10 * time.Second,
		ReadTimeout:       2 * time.Minute,
		WriteTimeout:      30 * time.Minute, // backup downloads
		TLSConfig:         &tls.Config{MinVersion: tls.VersionTLS12},
	}
	go func() {
		<-ctx.Done()
		sctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()
		_ = hs.Shutdown(sctx)
	}()
	log.Info("berth listening", "addr", cfg.Listen, "version", version)
	if err := hs.ListenAndServeTLS(certFile, keyFile); err != nil && !errors.Is(err, http.ErrServerClosed) {
		return err
	}
	return nil
}

func applyCmd(cfgPath string) error {
	cfg, st, err := open(cfgPath)
	if err != nil {
		return err
	}
	defer st.Close()
	eng := engine.New(cfg, st, slog.Default())
	if err := eng.Apply(context.Background()); err != nil {
		return err
	}
	fmt.Println("applied")
	return nil
}

func renderCmd(cfgPath string) error {
	cfg, st, err := open(cfgPath)
	if err != nil {
		return err
	}
	defer st.Close()
	ctx := context.Background()
	eng := engine.New(cfg, st, slog.Default())
	settings, err := st.Settings(ctx)
	if err != nil {
		return err
	}
	sites, err := st.Sites(ctx)
	if err != nil {
		return err
	}
	out, err := render.Render(eng.Paths(), settings, sites)
	if err != nil {
		return err
	}
	for path, content := range out.Files {
		fmt.Printf("### %s\n%s\n", path, content)
	}
	return nil
}

func readPassword(prompt string) (string, error) {
	if env := os.Getenv("BERTH_PASSWORD"); env != "" {
		return env, nil
	}
	fmt.Fprint(os.Stderr, prompt)
	if term.IsTerminal(int(os.Stdin.Fd())) {
		b, err := term.ReadPassword(int(os.Stdin.Fd()))
		fmt.Fprintln(os.Stderr)
		return string(b), err
	}
	line, err := bufio.NewReader(os.Stdin).ReadString('\n')
	return strings.TrimSpace(line), err
}

func passwd(cfgPath string) error {
	_, st, err := open(cfgPath)
	if err != nil {
		return err
	}
	defer st.Close()
	ctx := context.Background()
	u, err := st.FirstUser(ctx)
	if err != nil {
		return errors.New("no admin yet; open the web UI to create one")
	}
	pw, err := readPassword("New password for " + u.Username + ": ")
	if err != nil {
		return err
	}
	h, err := auth.HashPassword(pw)
	if err != nil {
		return err
	}
	u.PasswordHash, u.TOTPEnabled, u.TOTPSecret = h, false, ""
	if err := st.SaveUser(ctx, u); err != nil {
		return err
	}
	st.DeleteUserSessions(ctx, u.ID)
	fmt.Println("password reset for", u.Username, "(two-factor turned off)")
	return nil
}
