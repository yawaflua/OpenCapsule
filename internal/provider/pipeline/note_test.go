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

package pipeline

import (
	"context"
	"testing"
	"time"

	"github.com/yawaflua/marusya-gemini/internal/models"
	"github.com/yawaflua/marusya-gemini/internal/provider"
)

type stubSpeech struct{}

func (stubSpeech) Transcribe(context.Context, []byte, models.AudioFormat, string) (string, error) {
	return "стоп", nil
}
func (stubSpeech) Format() models.AudioFormat { return models.DefaultOutputFormat }
func (stubSpeech) Synthesize(_ context.Context, text, _ string) ([]byte, error) {
	return []byte(text), nil
}

// recordConv remembers the text each turn gave the model.
type recordConv struct{ got chan string }

func (c recordConv) Turn(_ context.Context, text string, out *Output) error {
	c.got <- out.WithNote(text)
	return nil
}

// TestNoteTurn checks that a note reaches the model with the next turn
// only, and that a turn abandoned before EndTurn does not leak it.
func TestNoteTurn(t *testing.T) {
	conv := recordConv{got: make(chan string, 4)}
	s := New(Config{Recognizer: stubSpeech{}, Synthesizer: stubSpeech{}, Options: provider.SessionOptions{InputFormat: models.DeviceInputFormat}}, conv)
	defer s.Close()
	go func() {
		for range s.Events() {
		}
	}()
	turn := func() string {
		t.Helper()
		s.StartTurn()
		s.SendAudio(make([]byte, 640))
		s.EndTurn()
		select {
		case text := <-conv.got:
			return text
		case <-time.After(2 * time.Second):
			t.Fatal("no turn")
			return ""
		}
	}

	s.NoteTurn("[играет музыка]")
	if got, want := turn(), "[играет музыка]\nстоп"; got != want {
		t.Errorf("noted turn = %q, want %q", got, want)
	}
	if got := turn(); got != "стоп" {
		t.Errorf("next turn = %q, want no note", got)
	}

	// Noted, started and cancelled: the next turn has no note.
	s.NoteTurn("[играет музыка]")
	s.StartTurn()
	if got := turn(); got != "стоп" {
		t.Errorf("turn after a cancelled one = %q, want no note", got)
	}
}
