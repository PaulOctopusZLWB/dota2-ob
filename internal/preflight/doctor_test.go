package preflight_test

import (
	"errors"
	"net"
	"os"
	"path/filepath"
	"reflect"
	"testing"

	"github.com/PaulOctopusZLWB/dota2-ob/internal/preflight"
)

type fakeListener struct{ closed bool }

func (l *fakeListener) Accept() (net.Conn, error) { return nil, errors.New("unused") }
func (l *fakeListener) Close() error              { l.closed = true; return nil }
func (l *fakeListener) Addr() net.Addr            { return fakeAddr("127.0.0.1:43210") }

type fakeAddr string

func (a fakeAddr) Network() string { return "tcp" }
func (a fakeAddr) String() string  { return string(a) }

func TestDoctorProducesOrderedPassesAndClosesListener(t *testing.T) {
	root := t.TempDir()
	dashboard := filepath.Join(root, "web", "index.html")
	if err := os.MkdirAll(filepath.Dir(dashboard), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(dashboard, []byte("dashboard"), 0o644); err != nil {
		t.Fatal(err)
	}
	config := filepath.Join(root, "gsi.cfg")
	if err := os.WriteFile(config, []byte(`"cfg" { "uri" "http://localhost:43210/gsi" }`), 0o644); err != nil {
		t.Fatal(err)
	}
	listener := &fakeListener{}
	doctor := preflight.NewDoctor(preflight.Dependencies{
		Listen:   func(string, string) (net.Listener, error) { return listener, nil },
		ReadFile: os.ReadFile, MkdirAll: os.MkdirAll, CreateTemp: os.CreateTemp, Remove: os.Remove,
	})
	result := doctor.Run(preflight.DoctorConfig{Address: "localhost:43210", DataRoot: filepath.Join(root, "data"), DashboardPath: dashboard, GSIConfig: config})
	if !result.OK || !listener.closed {
		t.Fatalf("result=%#v listener.closed=%v", result, listener.closed)
	}
	var ids []string
	for _, check := range result.Checks {
		ids = append(ids, check.ID)
		if check.Status != preflight.Pass {
			t.Fatalf("check=%#v", check)
		}
	}
	want := []string{"listen_address", "listen_available", "data_root", "dashboard_asset", "gsi_config"}
	if !reflect.DeepEqual(ids, want) {
		t.Fatalf("ids=%v, want %v", ids, want)
	}
}

func TestDoctorMissingDiscoveredConfigWarnsButRequiredFailuresFail(t *testing.T) {
	doctor := preflight.NewDoctor(preflight.Dependencies{
		Listen:     func(string, string) (net.Listener, error) { return nil, errors.New("occupied") },
		ReadFile:   func(string) ([]byte, error) { return nil, os.ErrNotExist },
		MkdirAll:   func(string, os.FileMode) error { return errors.New("unwritable") },
		CreateTemp: os.CreateTemp, Remove: os.Remove,
	})
	result := doctor.Run(preflight.DoctorConfig{Address: "127.0.0.1:43210", DataRoot: "ignored", DashboardPath: "missing", KnownConfigs: []string{"missing"}})
	if result.OK {
		t.Fatalf("result unexpectedly OK: %#v", result)
	}
	if result.Checks[1].Status != preflight.Fail || result.Checks[2].Status != preflight.Fail || result.Checks[3].Status != preflight.Fail || result.Checks[4].Status != preflight.Warning {
		t.Fatalf("checks=%#v", result.Checks)
	}
	for _, c := range result.Checks {
		if len(c.Message) > 160 {
			t.Fatalf("unbounded message")
		}
	}
}

func TestDoctorExplicitInvalidConfigFailsAfterClosingAcquiredListener(t *testing.T) {
	root := t.TempDir()
	dashboard := filepath.Join(root, "index.html")
	config := filepath.Join(root, "invalid.cfg")
	if err := os.WriteFile(dashboard, []byte("dashboard"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(config, []byte(`"cfg" { "uri" "http://127.0.0.1:9999/wrong" }`), 0o644); err != nil {
		t.Fatal(err)
	}
	listener := &fakeListener{}
	doctor := preflight.NewDoctor(preflight.Dependencies{
		Listen:   func(string, string) (net.Listener, error) { return listener, nil },
		ReadFile: os.ReadFile, MkdirAll: os.MkdirAll, CreateTemp: os.CreateTemp, Remove: os.Remove,
	})
	result := doctor.Run(preflight.DoctorConfig{Address: "127.0.0.1:43210", DataRoot: filepath.Join(root, "data"), DashboardPath: dashboard, GSIConfig: config})
	if result.OK || result.Checks[4].ID != "gsi_config" || result.Checks[4].Status != preflight.Fail {
		t.Fatalf("result=%#v", result)
	}
	if !listener.closed {
		t.Fatal("listener was not closed after later doctor failure")
	}
}
