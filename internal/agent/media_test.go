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

package agent_test

import (
	"context"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/yawaflua/marusya-gemini/internal/agent"
	"github.com/yawaflua/marusya-gemini/internal/controller"
	"github.com/yawaflua/marusya-gemini/internal/models"
	"github.com/yawaflua/marusya-gemini/internal/transport"
	"github.com/yawaflua/marusya-gemini/internal/wakeword"
)

type fakeMedia struct {
	mu      sync.Mutex
	playing bool
	ops     []models.MediaOperation
}

func (m *fakeMedia) Control(_ context.Context, op models.MediaOperation) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.ops = append(m.ops, op)
	switch op {
	case models.MediaPause, models.MediaStop:
		m.playing = false
	case models.MediaPlay:
		m.playing = true
	}
	return nil
}

func (m *fakeMedia) PlayURL(context.Context, string) error { return nil }

func (m *fakeMedia) Status(context.Context) (models.MediaStatus, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	return models.MediaStatus{Playing: m.playing, Title: "Кино - Группа крови", Source: "spotifyd"}, nil
}

func (m *fakeMedia) opsCopy() []models.MediaOperation {
	m.mu.Lock()
	defer m.mu.Unlock()
	return slices.Clone(m.ops)
}

// TestAgentTurnMedia checks that turn_start tells the server what played
// before the agent ducked the music, also on a follow-up turn while the
// music is still ducked.
func TestAgentTurnMedia(t *testing.T) {
	log := slog.New(slog.NewTextHandler(newTestWriter(t), &slog.HandlerOptions{Level: slog.LevelDebug}))
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	starts := make(chan *models.Message, 4)
	ends := make(chan struct{}, 4)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		conn, err := transport.Accept(w, r)
		if err != nil {
			return
		}
		defer conn.Close("bye")
		if f, err := conn.Read(ctx); err != nil || f.Message == nil || !slices.Contains(f.Message.Hello.Features, models.FeatureTurnMedia) {
			t.Error("hello without the turn_media feature")
			return
		}
		conn.Send(ctx, &models.Message{Type: models.TypeReady, Ready: &models.Ready{SessionID: "s", OutputFormat: models.DefaultOutputFormat}})
		for {
			f, err := conn.Read(ctx)
			if err != nil {
				return
			}
			switch m := f.Message; {
			case m == nil:
			case m.Type == models.TypeTurnStart:
				starts <- m
			case m.Type == models.TypeTurnEnd:
				// A reply, then the agent listens for a follow-up.
				conn.SendAudio(ctx, make([]byte, 4800))
				conn.Send(ctx, &models.Message{Type: models.TypeTurnComplete, TurnID: m.TurnID})
				ends <- struct{}{}
			}
		}
	}))
	defer srv.Close()

	mic := &fakeMic{}
	wake := make(fakeWake, 1)
	media := &fakeMedia{playing: true}
	a := agent.New(agent.Config{
		DeviceID:  "test",
		ServerURL: "ws" + strings.TrimPrefix(srv.URL, "http") + transport.Path,
		FollowUp:  true,
		DuckMedia: true,
	}, log, agent.Deps{Capture: mic, Player: &fakePlayer{done: make(chan struct{})}, Detector: wake, Dispatcher: &controller.Dispatcher{Media: media}, Indicator: &fakeIndicator{}})
	stopped := make(chan struct{})
	go func() {
		a.Run(ctx)
		close(stopped)
	}()
	defer func() {
		cancel()
		<-stopped
	}()

	time.Sleep(300 * time.Millisecond) // connect and learn the noise floor
	wake <- wakeword.Detection{Source: "test"}
	for turn := range 2 {
		time.Sleep(50 * time.Millisecond)
		mic.speechFrames.Store(40)
		select {
		case m := <-starts:
			want := models.MediaStatus{Playing: true, Title: "Кино - Группа крови", Source: "spotifyd"}
			if m.Media == nil || *m.Media != want {
				t.Errorf("turn %d: media = %+v, want %+v", turn, m.Media, want)
			}
		case <-ctx.Done():
			t.Fatalf("turn %d: no turn_start", turn)
		}
		select {
		case <-ends:
		case <-ctx.Done():
			t.Fatalf("turn %d: no turn_end", turn)
		}
	}
	if ops := media.opsCopy(); len(ops) == 0 || ops[0] != models.MediaPause {
		t.Errorf("media operations = %v, want the music paused first", ops)
	}
}
