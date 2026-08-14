package main

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"

	"github.com/PaulOctopusZLWB/dota2-ob/internal/capture/v3fixture"
	"github.com/PaulOctopusZLWB/dota2-ob/internal/liveprojection"
	"github.com/PaulOctopusZLWB/dota2-ob/internal/session"
)

type report struct {
	Mode              string `json:"mode"`
	RawBytes          int    `json:"raw_bytes"`
	RawSHA256         string `json:"raw_sha256"`
	EncodedBytes      int64  `json:"encoded_bytes,omitempty"`
	ProjectedSequence uint64 `json:"projected_sequence,omitempty"`
}
type counter struct{ calls int }

func (p *counter) Apply(context.Context, *session.Record) error { p.calls++; return nil }

func main() {
	if len(os.Args) != 4 {
		fatal("usage: v3-measure fixture|fixture-adversarial|append|recover INPUT OUTPUT")
	}
	mode, input, output := os.Args[1], os.Args[2], os.Args[3]
	switch mode {
	case "fixture":
		body, err := v3fixture.MaximumRelevantBody()
		check(err)
		check(os.WriteFile(output, body, 0o600))
		emit(mode, body, 0, 0)
	case "fixture-adversarial":
		body, err := v3fixture.RecognizedUnknownBody()
		check(err)
		check(os.WriteFile(output, body, 0o600))
		emit(mode, body, 0, 0)
	case "append":
		body, err := os.ReadFile(input)
		check(err)
		store, err := session.NewStore(output, session.WithSessionID("measure"))
		check(err)
		_, err = store.Append(body)
		check(err)
		check(store.Close())
		info, err := os.Stat(store.RawPath())
		check(err)
		emit(mode, body, info.Size(), 0)
	case "recover":
		body, err := os.ReadFile(input)
		check(err)
		projection := &counter{}
		f := liveprojection.New("measure", filepath.Join(output, "measure", "raw.jsonl"), filepath.Join(output, "measure", "missing-cursor.json"), []liveprojection.Projection{projection})
		check(f.CatchUp(context.Background(), 1))
		if projection.calls != 1 {
			fatal("recovery did not produce exactly one output")
		}
		emit(mode, body, 0, f.Health().ProjectedSequence)
	default:
		fatal("unknown mode")
	}
}
func emit(mode string, body []byte, encoded int64, projected uint64) {
	sum := sha256.Sum256(body)
	check(json.NewEncoder(os.Stdout).Encode(report{Mode: mode, RawBytes: len(body), RawSHA256: hex.EncodeToString(sum[:]), EncodedBytes: encoded, ProjectedSequence: projected}))
}
func check(err error) {
	if err != nil {
		fatal(err.Error())
	}
}
func fatal(message string) { fmt.Fprintln(os.Stderr, message); os.Exit(1) }
