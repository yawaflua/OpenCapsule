// Copyright 2026 The Marusya-Gemini Authors.
//
// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.
// You may obtain a copy of the License at
//
//	https://www.apache.org/licenses/LICENSE-2.0
//
// Unless required by applicable law or agreed to in writing, software
// distributed under the License is distributed on an "AS IS" BASIS,
// WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
// See the License for the specific language governing permissions and
// limitations under the License.

package server

import (
	"context"
	"log/slog"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/yawaflua/marusya-gemini/internal/models"
	"github.com/yawaflua/marusya-gemini/internal/provider"
	"github.com/yawaflua/marusya-gemini/internal/transport"
)

func TestMediaNote(t *testing.T) {
	for _, tc := range []struct {
		st   *models.MediaStatus
		want []string // substrings; nil means no note
	}{
		{nil, nil},
		{&models.MediaStatus{}, nil},
		{&models.MediaStatus{Playing: true, Title: "Кино - Группа крови", Source: "spotifyd"},
			[]string{"сейчас играет музыка «Кино - Группа крови»", "источник: Spotify (spotifyd)", "не вызывая инструментов"}},
		{&models.MediaStatus{Title: "Радио Маяк", Source: "stream"},
			[]string{"музыка на паузе «Радио Маяк»", "YouTube или радио через сервер"}},
		{&models.MediaStatus{Playing: true, Source: "vlc"}, []string{"сейчас играет музыка, источник: vlc"}},
	} {
		got := mediaNote(tc.st)
		if tc.want == nil {
			if got != "" {
				t.Errorf("mediaNote(%+v) = %q, want none", tc.st, got)
			}
			continue
		}
		for _, w := range tc.want {
			if !strings.Contains(got, w) {
				t.Errorf("mediaNote(%+v) = %q, want %q in it", tc.st, got, w)
			}
		}
	}
}

// noteProvider records the notes and turn starts of its sessions in order.
type noteProvider struct{ log chan string }

func (p *noteProvider) Name() string                     { return "fake" }
func (p *noteProvider) OutputFormat() models.AudioFormat { return models.DefaultOutputFormat }
func (p *noteProvider) Connect(context.Context, provider.SessionOptions) (provider.Session, error) {
	return &noteSession{p: p, events: make(chan provider.Event, 8)}, nil
}

type noteSession struct {
	p      *noteProvider
	events chan provider.Event
}

func (s *noteSession) NoteTurn(note string) error                  { s.p.log <- "note " + note; return nil }
func (s *noteSession) StartTurn() error                            { s.p.log <- "start"; return nil }
func (s *noteSession) SendAudio([]byte) error                      { return nil }
func (s *noteSession) EndTurn() error                              { return nil }
func (s *noteSession) SendToolResults([]provider.ToolResult) error { return nil }
func (s *noteSession) Events() <-chan provider.Event               { return s.events }
func (s *noteSession) Err() error                                  { return nil }
func (s *noteSession) Close() error                                { return nil }

// TestTurnMediaNote checks that what the speaker plays reaches the model
// before the turn, and that turns of older agents, without the field, get
// no note.
func TestTurnMediaNote(t *testing.T) {
	p := &noteProvider{log: make(chan string, 8)}
	srv := httptest.NewServer(New(Config{Provider: p, Logger: slog.New(slog.DiscardHandler)}))
	defer srv.Close()
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	conn, err := transport.Dial(ctx, "ws"+strings.TrimPrefix(srv.URL, "http")+transport.Path, "")
	if err != nil {
		t.Fatal(err)
	}
	defer conn.Close("done")
	conn.Send(ctx, &models.Message{Type: models.TypeHello, Hello: &models.Hello{
		ProtocolVersion: models.ProtocolVersion, DeviceID: "d", InputFormat: models.DeviceInputFormat,
		Features: []models.Feature{models.FeatureTurnMedia},
	}})
	if f, err := conn.Read(ctx); err != nil || f.Message.Type != models.TypeReady {
		t.Fatalf("want ready: %+v %v", f.Message, err)
	}
	next := func() string {
		t.Helper()
		select {
		case s := <-p.log:
			return s
		case <-ctx.Done():
			t.Fatal("provider got nothing")
			return ""
		}
	}

	conn.Send(ctx, &models.Message{Type: models.TypeTurnStart, TurnID: "t1",
		Media: &models.MediaStatus{Playing: true, Title: "Кино - Группа крови", Source: "mpv"}})
	if got := next(); !strings.HasPrefix(got, "note [Колонка: сейчас играет музыка «Кино - Группа крови»") {
		t.Errorf("first call = %q, want the note", got)
	}
	if got := next(); got != "start" {
		t.Errorf("then %q, want start", got)
	}

	// An older agent sends no media: just the turn.
	conn.Send(ctx, &models.Message{Type: models.TypeTurnCancel, TurnID: "t1"})
	conn.Send(ctx, &models.Message{Type: models.TypeTurnStart, TurnID: "t2"})
	if got := next(); got != "start" {
		t.Errorf("turn without media: %q, want start only", got)
	}
}
