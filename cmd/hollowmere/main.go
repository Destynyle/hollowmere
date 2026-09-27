// Command hollowmere runs the Hollowmere game server: the browser client
// over WebSocket, the RFC 42TAP TCP transport, and the operational
// endpoints.
package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"io"
	"io/fs"
	"net"
	"net/http"
	"os"
	"os/signal"
	"strconv"
	"strings"
	"syscall"
	"time"

	"hollowmere/internal/game"
	"hollowmere/internal/logs"
	"hollowmere/internal/session"
	"hollowmere/internal/store"
	"hollowmere/internal/tcpd"
	"hollowmere/internal/web"
	"hollowmere/internal/webd"
)

// env returns the environment value or a default, so the same binary is
// configured by flags in development and by the environment in Docker.
func env(key, def string) string {
	if v := os.Getenv(key); v != "" {
		return v
	}
	return def
}

func main() {
	var (
		httpAddr = flag.String("http", env("TAP_HTTP", "0.0.0.0:8080"), "address for the web client, WebSocket and metrics")
		tcpAddr  = flag.String("tcp", env("TAP_TCP", "0.0.0.0:4243"), "address for the raw TCP transport (empty to disable)")
		worldDir = flag.String("world", env("TAP_WORLD", "data/world"), "world directory (one JSON file per zone) or single world file")
		logFile  = flag.String("log-file", env("TAP_LOG_FILE", ""), "also append JSON logs to this file")
		logLevel = flag.String("log-level", env("TAP_LOG_LEVEL", "info"), "debug, info, warn or error")
		origins  = flag.String("origins", env("TAP_ORIGINS", ""), "comma-separated hosts allowed to open a WebSocket")
		trustPrx = flag.Bool("trust-proxy", env("TAP_TRUST_PROXY", "") == "1", "read the client IP from proxy headers")
		metricsT = flag.String("metrics-token", env("TAP_METRICS_TOKEN", ""), "require this token on /metrics (Bearer header or ?token=)")
		metricsF = flag.String("metrics-token-file", env("TAP_METRICS_TOKEN_FILE", ""), "read the /metrics token from this file (shared with Prometheus)")
		dbPath   = flag.String("db", env("TAP_DB", "state/hollowmere.db"), "character database (\"none\" disables persistence)")
		autosave = flag.Duration("autosave", 60*time.Second, "how often connected characters are saved")
		backupTo = flag.String("backup", "", "copy the database to this path and exit")
		grant    = flag.String("grant", "", "give a role and exit: NAME:moderator, NAME:admin or NAME:none")
		admins   = flag.Bool("admins", false, "list moderators and admins, then exit")
		listSanc = flag.Bool("sanctions", false, "list the bans and mutes in force, then exit")
		unban    = flag.String("unban", "", "lift the bans of a character and exit")
		verifyDB = flag.Bool("verify-db", false, "check the database integrity and exit (used by restore.sh)")
		check    = flag.Bool("check", false, "validate the world file and exit")
		seed     = flag.Int64("seed", 0, "random seed (0 = time based)")
	)
	flag.Parse()

	outs := []io.Writer{os.Stdout}
	if *logFile != "" {
		f, err := os.OpenFile(*logFile, os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0o644)
		if err != nil {
			fmt.Fprintln(os.Stderr, "cannot open log file:", err)
			os.Exit(1)
		}
		defer f.Close()
		outs = append(outs, f)
	}
	w := logs.NewAsyncWriter(outs...)
	defer w.Close()
	log := logs.New(w, logs.ParseLevel(*logLevel))

	// Backup mode: used by cron, safe to run while the server plays.
	if *backupTo != "" {
		st, err := store.Open(*dbPath)
		if err != nil {
			fmt.Fprintln(os.Stderr, err)
			os.Exit(1)
		}
		defer st.Close()
		if err := st.Backup(*backupTo); err != nil {
			fmt.Fprintln(os.Stderr, err)
			os.Exit(1)
		}
		n, _ := st.Count()
		fmt.Fprintf(os.Stderr, "backup written to %s (%d characters)\n", *backupTo, n)
		return
	}

	if *verifyDB {
		st, err := store.Open(*dbPath)
		if err != nil {
			fmt.Fprintln(os.Stderr, err)
			os.Exit(1)
		}
		n, err := st.Verify()
		st.Close()
		if err != nil {
			fmt.Fprintln(os.Stderr, err)
			os.Exit(1)
		}
		fmt.Fprintf(os.Stderr, "database OK: %d characters\n", n)
		return
	}

	// Console moderation: run next to the live server (SQLite handles the
	// concurrency); changes reach it at the next autosave or login.
	if *grant != "" || *admins || *listSanc || *unban != "" {
		if err := console(*dbPath, *grant, *admins, *listSanc, *unban); err != nil {
			fmt.Fprintln(os.Stderr, err)
			os.Exit(1)
		}
		return
	}

	world, err := game.LoadWorld(*worldDir)
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
	warnings := world.Warnings()
	if *check {
		for _, w := range warnings {
			fmt.Fprintln(os.Stderr, "warning:", w)
		}
		fmt.Fprintf(os.Stderr, "world OK: %d rooms, %d items, %d npcs, %d quests, %d warnings\n",
			len(world.Rooms), len(world.Items), len(world.NPCs), len(world.Quests), len(warnings))
		return
	}
	for _, w := range warnings {
		log.Warn("world_warning", "warning", w)
	}
	log.Info("world_loaded", "path", *worldDir, "rooms", len(world.Rooms), "items", len(world.Items),
		"npcs", len(world.NPCs), "quests", len(world.Quests), "warnings", len(warnings))

	if *seed == 0 {
		*seed = time.Now().UnixNano()
	}
	g := game.New(world, log, *seed)

	// Persistence: characters come back through their resume key. Without
	// a database the world simply resets when the server restarts.
	stopSaver := make(chan struct{})
	if *dbPath != "none" {
		st, err := store.Open(*dbPath)
		if err != nil {
			fmt.Fprintln(os.Stderr, err)
			os.Exit(1)
		}
		defer st.Close()
		n, _ := st.Count()
		log.Info("store_opened", "path", st.Path(), "characters", n, "autosave", autosave.String())
		g.SetStore(st)
		go g.AutoSave(*autosave, stopSaver)
	} else {
		log.Warn("store_disabled", "reason", "-db none")
	}

	mgr := session.NewManager(g, log, session.DefaultConfig(), session.NewMetrics())

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	assets, err := fs.Sub(web.Assets, ".")
	if err != nil {
		log.Error("assets_failed", "error", err.Error())
		os.Exit(1)
	}
	webCfg := webd.DefaultConfig()
	webCfg.TrustProxy = *trustPrx
	webCfg.MetricsToken = *metricsT
	if *metricsF != "" {
		raw, err := os.ReadFile(*metricsF)
		if err != nil {
			log.Error("metrics_token_file", "error", err.Error())
			os.Exit(1)
		}
		webCfg.MetricsToken = strings.TrimSpace(string(raw))
		if webCfg.MetricsToken == "" {
			log.Error("metrics_token_file", "error", "empty token file")
			os.Exit(1)
		}
	}
	if *origins != "" {
		webCfg.Origins = strings.Split(*origins, ",")
	}
	httpSrv := &http.Server{
		Addr:              *httpAddr,
		Handler:           webd.New(mgr, log, webCfg, assets).Handler(),
		ReadHeaderTimeout: 10 * time.Second,
	}

	errc := make(chan error, 2)
	go func() {
		log.Info("http_listening", "addr", *httpAddr, "trust_proxy", *trustPrx)
		if err := httpSrv.ListenAndServe(); err != nil && !errors.Is(err, http.ErrServerClosed) {
			errc <- fmt.Errorf("http: %w", err)
		}
	}()

	if *tcpAddr != "" {
		ln, err := net.Listen("tcp", *tcpAddr)
		if err != nil {
			log.Error("tcp_listen_failed", "addr", *tcpAddr, "error", err.Error())
			os.Exit(1)
		}
		go func() {
			if err := tcpd.New(mgr, log, tcpd.DefaultConfig()).Serve(ctx, ln); err != nil {
				errc <- fmt.Errorf("tcp: %w", err)
			}
		}()
	}

	select {
	case <-ctx.Done():
		log.Info("server_stopping", "reason", "signal")
	case err := <-errc:
		log.Error("server_error", "error", err.Error())
	}

	// Let players know, then give the writers a moment to flush.
	g.Announce("SHUTDOWN server restarting, come back in a minute")
	close(stopSaver)
	mgr.CloseAll("server_shutdown")
	if n := g.SaveAll(); n > 0 {
		log.Info("final_save", "players", n)
	}
	shutCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	_ = httpSrv.Shutdown(shutCtx)
	log.Info("server_stopped", "dropped_log_records", strconv.FormatInt(w.Dropped(), 10))
}

// console runs the moderation flags against the database.
func console(dbPath, grant string, admins, listSanc bool, unban string) error {
	st, err := store.Open(dbPath)
	if err != nil {
		return err
	}
	defer st.Close()
	if grant != "" {
		name, role, ok := strings.Cut(grant, ":")
		switch {
		case !ok || name == "":
			return fmt.Errorf("-grant expects NAME:moderator, NAME:admin or NAME:none")
		case role == "none":
			role = ""
		case role != "moderator" && role != "admin":
			return fmt.Errorf("unknown role %q", role)
		}
		if err := st.SetRole(name, role); err != nil {
			return err
		}
		fmt.Fprintf(os.Stderr, "%s: role %q (takes effect at their next login)\n", name, role)
	}
	if unban != "" {
		key, err := st.NameOwner(unban)
		if err != nil {
			return err
		}
		if key == "" {
			return fmt.Errorf("no character called %q", unban)
		}
		n, err := st.LiftSanctions("ban", key)
		if err != nil {
			return err
		}
		_ = st.Log("console", "unban", unban, fmt.Sprintf("%d lifted", n))
		fmt.Fprintf(os.Stderr, "%s: %d ban(s) lifted (the server notices within a minute)\n", unban, n)
	}
	if admins {
		list, err := st.Admins()
		if err != nil {
			return err
		}
		for _, a := range list {
			fmt.Printf("%-10s %s\n", a.Role, a.Name)
		}
	}
	if listSanc {
		list, err := st.ActiveSanctions(time.Now())
		if err != nil {
			return err
		}
		for _, x := range list {
			until := "permanent"
			if !x.Until.IsZero() {
				until = x.Until.Format(time.RFC3339)
			}
			fmt.Printf("#%d %-4s %-16s until %-25s by %-16s ip=%t %s\n", x.ID, x.Kind, x.Name, until, x.By, x.IP != "", x.Reason)
		}
	}
	return nil
}
