package main

import (
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"io"
	"log"
	"net"
	"net/http"
	"os"
	"os/signal"
	"path/filepath"
	"strings"
	"syscall"
	"time"

	"github.com/PaulOctopusZLWB/dota2-ob/internal/analytics"
	"github.com/PaulOctopusZLWB/dota2-ob/internal/gsi"
	"github.com/PaulOctopusZLWB/dota2-ob/internal/lifecycle"
	"github.com/PaulOctopusZLWB/dota2-ob/internal/operator"
	"github.com/PaulOctopusZLWB/dota2-ob/internal/preflight"
	"github.com/PaulOctopusZLWB/dota2-ob/internal/profile"
	"github.com/PaulOctopusZLWB/dota2-ob/internal/session"
	"github.com/PaulOctopusZLWB/dota2-ob/internal/state"
)

func main() { os.Exit(run(os.Args[1:], os.Stderr)) }

func run(args []string, output io.Writer) int {
	flags := flag.NewFlagSet("dota2-ob", flag.ContinueOnError)
	flags.SetOutput(output)
	addr := flags.String("addr", "127.0.0.1:43210", "HTTP listen address")
	dataDir := flags.String("data-dir", "./data/sessions", "directory for captured session data")
	analyzeSession := flags.String("analyze-session", "", "offline: analyze a session directory and exit")
	doctorMode := flags.Bool("doctor", false, "run one-shot operator readiness checks and exit")
	gsiConfig := flags.String("gsi-config", "", "explicit Dota 2 GSI config path for doctor mode")
	staleThreshold := flags.Duration("stale-threshold", 15*time.Second, "duration without accepted GSI before status becomes stale")
	if err := flags.Parse(args); err != nil {
		return 2
	}
	logger := log.New(output, "", log.LstdFlags)
	if strings.TrimSpace(*analyzeSession) != "" {
		if err := runAnalyze(*analyzeSession); err != nil {
			logger.Printf("analyze_session_failed")
			return 1
		}
		return 0
	}

	normalized, err := preflight.NormalizeListenAddress(*addr)
	if err != nil {
		fmt.Fprintln(output, err.Error())
		return 1
	}
	if *doctorMode {
		result := preflight.NewDoctor(preflight.Dependencies{}).Run(preflight.DoctorConfig{
			Address: normalized, DataRoot: *dataDir, DashboardPath: filepath.Join("web", "index.html"),
			GSIConfig: *gsiConfig, KnownConfigs: knownGSIConfigs(),
		})
		_ = json.NewEncoder(output).Encode(result)
		if result.OK {
			return 0
		}
		return 1
	}
	if *staleThreshold <= 0 {
		fmt.Fprintln(output, "stale_threshold_invalid")
		return 1
	}

	store, err := session.NewStore(*dataDir)
	if err != nil {
		logger.Printf("raw_store_create_failed")
		return 1
	}
	listener, err := net.Listen("tcp", normalized)
	if err != nil {
		_ = store.Close()
		logger.Printf("server_listen_failed")
		return 1
	}
	now := time.Now().UTC()
	tracker := operator.NewTracker(store.SessionID(), now, *staleThreshold, time.Now)
	handler := gsi.NewServer(store,
		gsi.WithLatest(state.NewLatest()), gsi.WithProfiler(profile.NewProfiler()),
		gsi.WithAnalytics(analytics.NewEngine()), gsi.WithTracker(tracker),
		gsi.WithDashboard(http.FileServer(http.Dir("web"))),
	)
	server := &http.Server{Handler: handler, ReadHeaderTimeout: 5 * time.Second}
	logger.Printf("server_started addr=%s session_id=%s capture_target=%s", normalized, store.SessionID(), filepath.Join(store.SessionID(), "raw.jsonl"))
	signals := make(chan os.Signal, 2)
	signal.Notify(signals, os.Interrupt, syscall.SIGTERM)
	defer signal.Stop(signals)
	err = lifecycle.Run(server, listener, store, signals, func() (context.Context, context.CancelFunc) {
		return context.WithTimeout(context.Background(), 10*time.Second)
	})
	if err != nil {
		logger.Printf("server_exit_failed codes=%s", err)
		return 1
	}
	logger.Printf("server_stopped")
	return 0
}

func knownGSIConfigs() []string {
	home, err := os.UserHomeDir()
	if err != nil {
		return nil
	}
	rel := filepath.Join("steamapps", "common", "dota 2 beta", "game", "dota", "cfg", "gamestate_integration", "gamestate_integration_dota2_ob.cfg")
	return []string{filepath.Join(home, ".local", "share", "Steam", rel), filepath.Join(home, ".steam", "steam", rel)}
}

func runAnalyze(sessionPath string) error {
	abs, err := filepath.Abs(sessionPath)
	if err != nil {
		return err
	}
	info, err := os.Stat(abs)
	if err != nil {
		return err
	}
	if !info.IsDir() {
		return fmt.Errorf("analyze-session path is not a directory")
	}
	_, err = analytics.AnalyzeSession(abs, filepath.Base(abs))
	return err
}
