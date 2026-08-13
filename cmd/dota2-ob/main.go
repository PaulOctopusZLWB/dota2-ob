package main

import (
	"context"
	"crypto/rand"
	"encoding/base64"
	"encoding/json"
	"errors"
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
	"github.com/PaulOctopusZLWB/dota2-ob/internal/capture"
	"github.com/PaulOctopusZLWB/dota2-ob/internal/delivery"
	"github.com/PaulOctopusZLWB/dota2-ob/internal/gsi"
	"github.com/PaulOctopusZLWB/dota2-ob/internal/lifecycle"
	"github.com/PaulOctopusZLWB/dota2-ob/internal/operator"
	"github.com/PaulOctopusZLWB/dota2-ob/internal/preflight"
	"github.com/PaulOctopusZLWB/dota2-ob/internal/profile"
	"github.com/PaulOctopusZLWB/dota2-ob/internal/session"
	"github.com/PaulOctopusZLWB/dota2-ob/internal/state"
	webassets "github.com/PaulOctopusZLWB/dota2-ob/web"
)

const operatorTokenFilename = "operator.token"

func main() { os.Exit(run(os.Args[1:], os.Stderr)) }

type runDependencies struct {
	newStore     func(string, string) (*session.Store, error)
	newTokenFile func(string) (string, string, func(), error)
	listen       func(string, string) (net.Listener, error)
	runLifecycle func(lifecycle.Server, net.Listener, lifecycle.Closer, lifecycle.Waiter, <-chan os.Signal, lifecycle.ContextFactory) error
	runDoctor    func(preflight.DoctorConfig) preflight.Result
}

func defaultRunDependencies() runDependencies {
	return runDependencies{
		newStore: func(root, sessionID string) (*session.Store, error) {
			if strings.TrimSpace(sessionID) == "" {
				return session.NewStore(root)
			}
			return session.NewStore(root, session.WithSessionID(sessionID))
		},
		newTokenFile: createEphemeralTokenFile,
		listen:       net.Listen,
		runLifecycle: lifecycle.Run,
		runDoctor:    preflight.NewDoctor(preflight.Dependencies{}).Run,
	}
}

func run(args []string, output io.Writer) int {
	return runWithDependencies(args, output, defaultRunDependencies())
}

func runWithDependencies(args []string, output io.Writer, deps runDependencies) int {
	flags := flag.NewFlagSet("dota2-ob", flag.ContinueOnError)
	flags.SetOutput(output)
	addr := flags.String("addr", "127.0.0.1:43210", "HTTP listen address")
	deliveryAddr := flags.String("delivery-addr", "127.0.0.1:43211", "operator/overlay loopback listen address")
	dataDir := flags.String("data-dir", "./data/sessions", "directory for captured session data")
	sessionID := flags.String("session-id", "", "explicit safe session identity for sealed policy lineage and restart")
	diagnosticMode := flags.Bool("diagnostic-mode", false, "enable authenticated legacy capture diagnostics")
	operatorTokenFile := flags.String("operator-token-file", "", "explicit external 0600 token handoff path for the operator process")
	policyLineageFile := flags.String("policy-lineage-file", "", "sealed PolicyLineageManifestV2 for the broadcast policy plane")
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

	if *doctorMode {
		result := deps.runDoctor(preflight.DoctorConfig{
			Address: *addr, DataRoot: *dataDir, DashboardPath: filepath.Join("web", "index.html"),
			GSIConfig: *gsiConfig, KnownConfigs: knownGSIConfigs(),
		})
		_ = json.NewEncoder(output).Encode(result)
		if result.OK {
			return 0
		}
		return 1
	}
	normalized, err := preflight.NormalizeListenAddress(*addr)
	if err != nil {
		fmt.Fprintln(output, err.Error())
		return 1
	}
	normalizedDelivery, err := preflight.NormalizeListenAddress(*deliveryAddr)
	if err != nil {
		fmt.Fprintln(output, err.Error())
		return 1
	}
	if normalizedDelivery == normalized {
		fmt.Fprintln(output, "listener_addresses_must_be_distinct")
		return 1
	}
	if *staleThreshold <= 0 {
		fmt.Fprintln(output, "stale_threshold_invalid")
		return 1
	}

	store, err := deps.newStore(*dataDir, *sessionID)
	if err != nil {
		logger.Printf("raw_store_create_failed")
		return 1
	}
	token, _, cleanupToken, err := deps.newTokenFile(*operatorTokenFile)
	if err != nil {
		_ = store.Close()
		logger.Printf("operator_token_create_failed")
		return 1
	}
	defer cleanupToken()
	listener, err := deps.listen("tcp", normalized)
	if err != nil {
		_ = store.Close()
		logger.Printf("server_listen_failed")
		return 1
	}
	deliveryListener, err := deps.listen("tcp", normalizedDelivery)
	if err != nil {
		logger.Printf("delivery_listen_failed")
		deliveryListener = nil
	}
	var broadcast *broadcastRuntime
	if deliveryListener != nil {
		lineage, lineageErr := loadPolicyLineage(*policyLineageFile, store.SessionID())
		if lineageErr == nil {
			broadcast, lineageErr = newBroadcastRuntime(broadcastConfig{
				DataRoot: *dataDir, SessionID: store.SessionID(), RawPath: store.RawPath(), Lineage: lineage, Now: time.Now,
			})
		}
		if lineageErr != nil {
			_ = deliveryListener.Close()
			deliveryListener = nil
			logger.Printf("broadcast_policy_config_failed")
		}
	}
	if broadcast != nil {
		defer func() {
			if err := broadcast.Close(); err != nil {
				logger.Printf("broadcast_policy_close_failed")
			}
		}()
	}
	now := time.Now().UTC()
	tracker := operator.NewTracker(store.SessionID(), now, *staleThreshold, time.Now)
	latest := state.NewLatest()
	profilerInstance := profile.NewProfiler()
	analyticsEngine := analytics.NewEngine()
	captureOptions := []gsi.Option{
		gsi.WithLatest(latest), gsi.WithProfiler(profilerInstance),
		gsi.WithAnalytics(analyticsEngine), gsi.WithTracker(tracker),
	}
	if broadcast != nil {
		captureOptions = append(captureOptions, gsi.WithLiveProjections(
			newTrackedCaptureProjection(capture.NewLatestProjection(latest), tracker, operator.SubsystemLatest, "latest_failed"),
			newTrackedCaptureProjection(capture.NewProfileProjection(profilerInstance, store.SessionDir()), tracker, operator.SubsystemProfile, "profile_failed"),
			newTrackedCaptureProjection(capture.NewAnalyticsProjection(analyticsEngine, store.SessionDir(), store.SessionID()), tracker, operator.SubsystemAnalytics, "analytics_failed"),
			newPolicyObservationProjection(broadcast, tracker),
		))
	}
	if *diagnosticMode {
		captureOptions = append(captureOptions,
			gsi.WithDashboard(http.FileServer(http.Dir("web"))),
			gsi.WithDiagnostics(gsi.DiagnosticConfig{BearerToken: token, AllowedOrigin: "http://" + normalized}),
		)
	}
	handler := gsi.NewServer(store, captureOptions...)
	server := &http.Server{Handler: handler, ReadHeaderTimeout: 5 * time.Second}
	var deliveryServer *http.Server
	if deliveryListener != nil {
		gateway, gatewayErr := delivery.NewGateway(delivery.Config{
			BearerToken: token, AllowedOrigin: "http://" + normalizedDelivery,
			Commands: broadcastCommandPort{broadcast}, Operator: broadcastOperatorPort{broadcast}, Overlay: broadcastOverlayPort{broadcast}, ReadAsset: webassets.ReadAsset, Now: time.Now,
		})
		if gatewayErr != nil {
			_ = deliveryListener.Close()
			deliveryListener = nil
			logger.Printf("delivery_config_failed")
		} else {
			deliveryServer = &http.Server{Handler: gateway, ReadHeaderTimeout: 5 * time.Second}
		}
	}
	servers := &pairedHTTPServer{
		capture: server, delivery: deliveryServer, deliveryListener: deliveryListener,
		reportDeliveryFailure: func(code string) { logger.Printf("%s", code) },
	}
	logger.Printf("server_started addr=%s delivery_addr=%s session_id=%s capture_target=%s", normalized, normalizedDelivery, store.SessionID(), filepath.Join(store.SessionID(), "raw.jsonl"))
	signals := make(chan os.Signal, 2)
	signal.Notify(signals, os.Interrupt, syscall.SIGTERM)
	defer signal.Stop(signals)
	err = deps.runLifecycle(servers, listener, store, handler, signals, func() (context.Context, context.CancelFunc) {
		return context.WithTimeout(context.Background(), 10*time.Second)
	})
	if err != nil {
		logger.Printf("server_exit_failed codes=%s", err)
		return 1
	}
	logger.Printf("server_stopped")
	return 0
}

type pairedHTTPServer struct {
	capture               *http.Server
	delivery              *http.Server
	deliveryListener      net.Listener
	reportDeliveryFailure func(string)
}

func (s *pairedHTTPServer) Serve(captureListener net.Listener) error {
	if s.delivery != nil && s.deliveryListener != nil {
		go func() {
			if err := s.delivery.Serve(s.deliveryListener); err != nil && !errors.Is(err, http.ErrServerClosed) {
				s.reportDelivery("delivery_serve_failed")
			}
		}()
	}
	return s.capture.Serve(captureListener)
}

func (s *pairedHTTPServer) Shutdown(ctx context.Context) error {
	captureErr := s.capture.Shutdown(ctx)
	if s.delivery != nil {
		if err := s.delivery.Shutdown(ctx); err != nil {
			s.reportDelivery("delivery_shutdown_failed")
			_ = s.delivery.Close()
		}
	}
	return captureErr
}

func (s *pairedHTTPServer) Close() error {
	captureErr := s.capture.Close()
	if s.delivery != nil {
		if err := s.delivery.Close(); err != nil {
			s.reportDelivery("delivery_close_failed")
		}
	}
	return captureErr
}

func (s *pairedHTTPServer) reportDelivery(code string) {
	if s.reportDeliveryFailure != nil {
		s.reportDeliveryFailure(code)
	}
}

func newEphemeralToken() (string, error) {
	data := make([]byte, 32)
	if _, err := rand.Read(data); err != nil {
		return "", err
	}
	return base64.RawURLEncoding.EncodeToString(data), nil
}

func createEphemeralTokenFile(explicitPath string) (string, string, func(), error) {
	if strings.TrimSpace(explicitPath) != "" {
		filename, err := filepath.Abs(explicitPath)
		if err != nil || filename != filepath.Clean(explicitPath) {
			return "", "", nil, errors.New("operator token path must be absolute and clean")
		}
		info, err := os.Stat(filepath.Dir(filename))
		if err != nil || !info.IsDir() || info.Mode().Perm()&0o077 != 0 {
			return "", "", nil, errors.New("operator token directory must be user-only")
		}
		token, err := newEphemeralToken()
		if err != nil {
			return "", "", nil, err
		}
		if err := writePrivateToken(filename, token); err != nil {
			return "", "", nil, err
		}
		return token, filename, func() { _ = os.Remove(filename) }, nil
	}
	base := os.Getenv("XDG_RUNTIME_DIR")
	if base == "" {
		cache, err := os.UserCacheDir()
		if err != nil {
			return "", "", nil, err
		}
		base = cache
	}
	directory := filepath.Join(base, "dota2-ob", "runtime")
	if err := os.MkdirAll(directory, 0o700); err != nil {
		return "", "", nil, err
	}
	if info, err := os.Stat(directory); err != nil || info.Mode().Perm()&0o077 != 0 {
		return "", "", nil, errors.New("default operator token directory must be user-only")
	}
	token, err := newEphemeralToken()
	if err != nil {
		return "", "", nil, err
	}
	filename := filepath.Join(directory, operatorTokenFilename)
	if err := writePrivateToken(filename, token); err != nil {
		return "", "", nil, err
	}
	cleanup := func() { _ = os.Remove(filename) }
	return token, filename, cleanup, nil
}

func writePrivateToken(filename, token string) error {
	file, err := os.OpenFile(filename, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o600)
	if err != nil {
		return err
	}
	_, writeErr := io.WriteString(file, token+"\n")
	closeErr := file.Close()
	if writeErr != nil {
		_ = os.Remove(filename)
		return writeErr
	}
	if closeErr != nil {
		_ = os.Remove(filename)
		return closeErr
	}
	return nil
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
	_, err = capture.AnalyzeSession(abs, filepath.Base(abs))
	return err
}
