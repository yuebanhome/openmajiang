package platform

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"testing"

	"github.com/jackc/pgx/v5"
	"github.com/yuebanhome/openmajiang/pkg/rulesdk"
	"github.com/yuebanhome/openmajiang/rules/registry"
)

type individualProjectionRule struct{ rulesdk.Rule }

type faultyBatchProjector struct {
	rulesdk.Rule
	views []json.RawMessage
	err   error
}

func (r faultyBatchProjector) ProjectMany(rulesdk.Snapshot, []rulesdk.Viewer) ([]json.RawMessage, error) {
	return r.views, r.err
}

func TestProjectFlowViewsUsesRegisteredBatchOrPluginFallback(t *testing.T) {
	r, err := registry.Default()
	if err != nil {
		t.Fatal(err)
	}
	m := r.List()[0]
	rule, _ := r.Get(m.ID, m.Version)
	if _, ok := rule.(rulesdk.BatchProjector); !ok {
		t.Fatal("registered MCR lost batch capability")
	}
	cfg := rulesdk.Config{Format: "practice_1", Profile: "om-mcr-1", Participants: []rulesdk.Participant{
		{ID: "A", Kind: "human"}, {ID: "B", Kind: "bot"}, {ID: "C", Kind: "bot"}, {ID: "D", Kind: "bot"},
	}}
	raw, err := rule.Init(cfg, bytes.Repeat([]byte{7}, 32))
	if err != nil {
		t.Fatal(err)
	}
	// A non-seat ordering detects accidental indexing by seat instead of viewer.
	assignments := []rulesdk.Assignment{{ParticipantID: "C", Seat: 2}, {ParticipantID: "A", Seat: 0}, {ParticipantID: "D", Seat: 3}, {ParticipantID: "B", Seat: 1}}
	batch, err := projectFlowViews(rule, raw, assignments)
	if err != nil {
		t.Fatal(err)
	}
	individual, err := projectFlowViews(individualProjectionRule{rule}, raw, assignments)
	if err != nil || len(individual) != len(batch) {
		t.Fatalf("legacy plugin fallback failed: %v", err)
	}
	for i := range batch {
		if !bytes.Equal(batch[i], individual[i]) {
			t.Fatalf("batch and fallback differ at viewer %d", i)
		}
	}
	if _, err := ValidateSpectator(batch[0]); err != nil {
		t.Fatalf("batch public projection violates discard-only contract: %v", err)
	}
	for _, candidate := range []rulesdk.Rule{rule, individualProjectionRule{rule}} {
		if views, err := projectFlowViews(candidate, raw, append(assignments, rulesdk.Assignment{ParticipantID: "intruder"})); err == nil || views != nil {
			t.Fatal("failed projection returned partial views")
		}
	}
}

func TestPersistFlowViewsRejectsInvalidBatchBeforeWriting(t *testing.T) {
	public := json.RawMessage(`{"view_policy":"spectator_discard_only@1","discards":[]}`)
	for name, rule := range map[string]faultyBatchProjector{
		"missing":        {views: nil},
		"extra":          {views: []json.RawMessage{public, public}},
		"private_public": {views: []json.RawMessage{json.RawMessage(`{"view_policy":"spectator_discard_only@1","discards":[],"hand":["1m"]}`)}},
		"error":          {views: []json.RawMessage{public}, err: errors.New("projection failure")},
	} {
		t.Run(name, func(t *testing.T) {
			// A nil transaction makes an accidental prevalidation write fail here.
			if err := (&Service{}).persistFlowViews(context.Background(), nil, match{}, rule, nil, rulesdk.Input{}, rulesdk.Flow{}); err == nil {
				t.Fatal("accepted invalid projection batch")
			}
		})
	}
}

type projectionRecordingTx struct {
	pgx.Tx
	batch *pgx.Batch
	err   error
}

func (tx *projectionRecordingTx) SendBatch(_ context.Context, batch *pgx.Batch) pgx.BatchResults {
	tx.batch = batch
	return projectionBatchResults{err: tx.err}
}

type projectionBatchResults struct {
	pgx.BatchResults
	err error
}

func (r projectionBatchResults) Close() error { return r.err }

func TestPersistBatchViewsKeepsPublicAndPrivateRowsSeparate(t *testing.T) {
	public := json.RawMessage(`{"view_policy":"spectator_discard_only@1","discards":[]}`)
	private := json.RawMessage(`{"view_policy":"participant_private@1","hand":["secret"]}`)
	rule := faultyBatchProjector{views: []json.RawMessage{public, private}}
	stopped := errors.New("batch recorded")
	tx := &projectionRecordingTx{err: stopped}
	flow := rulesdk.Flow{HandIndex: 3, Assignments: []rulesdk.Assignment{{ParticipantID: "A"}}}
	err := (&Service{}).persistFlowViews(context.Background(), tx, match{ID: "match", Seq: 8}, rule, nil, rulesdk.Input{}, flow)
	if !errors.Is(err, stopped) || tx.batch == nil || tx.batch.Len() != 3 {
		t.Fatalf("expected two views and one event: %v", err)
	}
	publicRow, privateRow := tx.batch.QueuedQueries[0], tx.batch.QueuedQueries[1]
	publicView := publicRow.Arguments[3].(json.RawMessage)
	if _, err := ValidateSpectator(publicView); err != nil || bytes.Contains(publicView, []byte("secret")) {
		t.Fatalf("private data entered public row: %v", err)
	}
	if privateRow.Arguments[3] != "A" || !bytes.Equal(privateRow.Arguments[4].(json.RawMessage), private) {
		t.Fatal("participant projection lost its separate recipient row")
	}
}
