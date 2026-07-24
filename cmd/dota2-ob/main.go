package main

import (
	"flag"
	"fmt"
	"log"
	"net/http"
	"os"
	"path/filepath"
	"strings"

	"github.com/PaulOctopusZLWB/dota2-ob/internal/analytics"
	"github.com/PaulOctopusZLWB/dota2-ob/internal/gsi"
	"github.com/PaulOctopusZLWB/dota2-ob/internal/profile"
	"github.com/PaulOctopusZLWB/dota2-ob/internal/session"
	"github.com/PaulOctopusZLWB/dota2-ob/internal/state"
)

func main() {
	addr := flag.String("addr", "127.0.0.1:43210", "HTTP listen address")
	dataDir := flag.String("data-dir", "./data/sessions", "directory for captured session data")
	analyzeSession := flag.String("analyze-session", "", "offline: analyze a session directory and write analytics artifacts, then exit")
	flag.Parse()

	if strings.TrimSpace(*analyzeSession) != "" {
		if err := runAnalyze(*analyzeSession); err != nil {
			log.Fatalf("analyze session: %v", err)
		}
		return
	}

	store, err := session.NewStore(*dataDir)
	if err != nil {
		log.Fatalf("create session store: %v", err)
	}
	latest := state.NewLatest()
	profiler := profile.NewProfiler()
	engine := analytics.NewEngine()

	log.Printf("dota2-ob listening on http://%s", *addr)
	log.Printf("capturing raw GSI snapshots under %s/%s", *dataDir, store.SessionID())
	if err := http.ListenAndServe(*addr, gsi.NewServer(
		store,
		gsi.WithLatest(latest),
		gsi.WithProfiler(profiler),
		gsi.WithAnalytics(engine),
		gsi.WithDashboard(http.FileServer(http.Dir("web"))),
	)); err != nil {
		log.Fatalf("server stopped: %v", err)
	}
}

// runAnalyze resolves the session directory and identifier from a CLI path and
// runs the offline analysis pipeline.
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
		return fmt.Errorf("analyze-session path is not a directory: %s", abs)
	}
	sessionID := filepath.Base(abs)
	log.Printf("analyzing session %s at %s", sessionID, abs)
	if _, err := analytics.AnalyzeSession(abs, sessionID); err != nil {
		return err
	}
	log.Printf("analysis complete for session %s", sessionID)
	return nil
}