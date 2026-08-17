package obscontrol_test

import (
	"context"
	"errors"
	"testing"

	"github.com/PaulOctopusZLWB/dota2-ob/internal/obscontrol"
)

type visibilityClient struct {
	calls  int
	scene  string
	source string
	shown  bool
	err    error
}

func (c *visibilityClient) SetSourceVisibility(_ context.Context, scene, source string, shown bool) error {
	c.calls++
	c.scene, c.source, c.shown = scene, source, shown
	return c.err
}

func TestAdapterAcceptsOnlyAuthenticatedLoopbackConfiguration(t *testing.T) {
	client := &visibilityClient{}
	for _, endpoint := range []string{"ws://127.0.0.1:4455", "ws://localhost:4455", "ws://[::1]:4455"} {
		if _, err := obscontrol.New(obscontrol.Config{Endpoint: endpoint, Password: "ephemeral-secret", Client: client}); err != nil {
			t.Errorf("loopback %q rejected: %v", endpoint, err)
		}
	}
	for _, endpoint := range []string{"wss://127.0.0.1:4455", "ws://192.0.2.1:4455", "ws://127.0.0.1:4455/path", "ws://user@127.0.0.1:4455"} {
		if _, err := obscontrol.New(obscontrol.Config{Endpoint: endpoint, Password: "ephemeral-secret", Client: client}); err == nil {
			t.Errorf("unsafe endpoint %q accepted", endpoint)
		}
	}
	if _, err := obscontrol.New(obscontrol.Config{Endpoint: "ws://127.0.0.1:4455", Client: client}); err == nil {
		t.Fatal("missing password accepted")
	}
}

func TestAdapterControlsOnlyAllowlistedSceneSourceVisibility(t *testing.T) {
	client := &visibilityClient{}
	adapter, err := obscontrol.New(obscontrol.Config{
		Endpoint: "ws://127.0.0.1:4455", Password: "ephemeral-secret", Client: client,
		AllowedScene: "Dota2-OB M3", AllowedSource: "Analytics Sidebar",
	})
	if err != nil {
		t.Fatal(err)
	}
	if err := adapter.SetVisible(context.Background(), true); err != nil {
		t.Fatal(err)
	}
	if client.calls != 1 || client.scene != "Dota2-OB M3" || client.source != "Analytics Sidebar" || !client.shown {
		t.Fatalf("unexpected visibility call: %#v", client)
	}
	client.err = errors.New("connection failed")
	if err := adapter.SetVisible(context.Background(), false); !errors.Is(err, obscontrol.ErrUnavailable) {
		t.Fatalf("error = %v, want unavailable", err)
	}
}

func TestAdapterHasNoAnalyticalMutationAPI(t *testing.T) {
	typ := interface{}(&obscontrol.Adapter{})
	if _, ok := typ.(interface {
		Execute(context.Context, any) error
	}); ok {
		t.Fatal("OBS adapter exposes analytical command execution")
	}
}
