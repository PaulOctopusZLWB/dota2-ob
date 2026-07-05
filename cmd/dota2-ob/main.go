package main

import (
	"flag"
	"log"
	"net/http"

	"github.com/PaulOctopusZLWB/dota2-ob/internal/gsi"
	"github.com/PaulOctopusZLWB/dota2-ob/internal/profile"
	"github.com/PaulOctopusZLWB/dota2-ob/internal/session"
	"github.com/PaulOctopusZLWB/dota2-ob/internal/state"
)

func main() {
	addr := flag.String("addr", "127.0.0.1:43210", "HTTP listen address")
	dataDir := flag.String("data-dir", "./data/sessions", "directory for captured session data")
	flag.Parse()

	store, err := session.NewStore(*dataDir)
	if err != nil {
		log.Fatalf("create session store: %v", err)
	}
	latest := state.NewLatest()
	profiler := profile.NewProfiler()

	log.Printf("dota2-ob listening on http://%s", *addr)
	log.Printf("capturing raw GSI snapshots under %s/%s", *dataDir, store.SessionID())
	if err := http.ListenAndServe(*addr, gsi.NewServer(
		store,
		gsi.WithLatest(latest),
		gsi.WithProfiler(profiler),
		gsi.WithDashboard(http.FileServer(http.Dir("web"))),
	)); err != nil {
		log.Fatalf("server stopped: %v", err)
	}
}
