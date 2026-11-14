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
package touchkeys

import (
	"context"
	"encoding/binary"
	"os"
	"path/filepath"
	"slices"
	"sync"
	"sync/atomic"
	"syscall"
	"testing"
	"time"

	"github.com/yawaflua/marusya-gemini/internal/controller"
	"github.com/yawaflua/marusya-gemini/internal/models"
	"github.com/yawaflua/marusya-gemini/internal/wakeword"
)

type fakeVolume struct {
	mu    sync.Mutex
	level int
}

func (v *fakeVolume) Get(context.Context) (models.VolumeState, error) {
	v.mu.Lock()
	defer v.mu.Unlock()
	return models.VolumeState{Level: v.level}, nil
}

func (v *fakeVolume) Set(_ context.Context, l int) error {
	v.mu.Lock()
	defer v.mu.Unlock()
	v.level = l
	return nil
}

func (v *fakeVolume) SetMuted(context.Context, bool) error { return nil }

type fakeBT struct{ paired chan struct{} }

func (b *fakeBT) Pair(context.Context, time.Duration) error { close(b.paired); return nil }
func (b *fakeBT) Connect(context.Context, string) error     { return nil }
func (b *fakeBT) Disconnect(context.Context, string) error  { return nil }
func (b *fakeBT) Devices(context.Context) ([]models.BluetoothDevice, error) {
	return nil, nil
}

type overlays struct {
	mu   sync.Mutex
	text []string
}

func (o *overlays) Overlay(s string, _ time.Duration) {
	o.mu.Lock()
	defer o.mu.Unlock()
	o.text = append(o.text, s)
}

func event(code uint16, value int32) []byte {
	b := make([]byte, 24)
	binary.LittleEndian.PutUint16(b[16:], 1) // EV_KEY
	binary.LittleEndian.PutUint16(b[18:], code)
	binary.LittleEndian.PutUint32(b[20:], uint32(value))
	return b
}

func TestPanel(t *testing.T) {
	fifo := filepath.Join(t.TempDir(), "event0")
	if err := syscall.Mkfifo(fifo, 0o600); err != nil {
		t.Skip("mkfifo:", err)
	}
	vol := &fakeVolume{level: 50}
	bt := &fakeBT{paired: make(chan struct{})}
	ov := &overlays{}
	var (
		muted  atomic.Bool
		keysMu sync.Mutex
		keys   []string
	)
	cfg := DefaultConfig()
	cfg.Device = fifo
	cfg.LongPress = 100 * time.Millisecond
	p := &Panel{
		Config:     cfg,
		Dispatcher: &controller.Dispatcher{Volume: vol, Bluetooth: bt},
		Display:    ov,
		OnMicMute:  func(m bool) { muted.Store(m) },
		OnKey: func(name string, _ uint16, pressed bool) {
			if pressed {
				keysMu.Lock()
				keys = append(keys, name)
				keysMu.Unlock()
			}
		},
	}

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	out := make(chan wakeword.Detection, 4)
	go p.Run(ctx, nil, out)

	w, err := os.OpenFile(fifo, os.O_WRONLY, 0)
	if err != nil {
		t.Fatal(err)
	}
	defer w.Close()
	send := func(code uint16, v int32) { w.Write(event(code, v)) }

	// Short centre press activates the assistant.
	send(KeyVoice, 1)
	send(KeyVoice, 0)
	select {
	case d := <-out:
		if d.Source != "touch" {
			t.Errorf("detection source = %q", d.Source)
		}
	case <-time.After(time.Second):
		t.Fatal("short press did not activate the assistant")
	}

	// Long centre press pairs and does not activate.
	send(KeyVoice, 1)
	select {
	case <-bt.paired:
	case <-time.After(time.Second):
		t.Fatal("long press did not start pairing")
	}
	send(KeyVoice, 0)

	// Volume up by one step.
	send(KeyVolumeUp, 1)
	send(KeyVolumeUp, 0)

	// Mic mute.
	send(KeyMicMute, 1)
	send(KeyMicMute, 0)

	time.Sleep(100 * time.Millisecond)
	if lvl, _ := vol.Get(ctx); lvl.Level != 55 {
		t.Errorf("volume = %d, want 55", lvl.Level)
	}
	if !muted.Load() {
		t.Error("microphone should be muted")
	}
	if len(out) != 0 {
		t.Error("long press must not activate the assistant")
	}
	keysMu.Lock()
	if want := []string{"voice", "voice", "volume_up", "mic"}; !slices.Equal(keys, want) {
		t.Errorf("OnKey presses = %v, want %v", keys, want)
	}
	keysMu.Unlock()
	ov.mu.Lock()
	defer ov.mu.Unlock()
	// Muting shows no text: the ring and the face show it.
	if want := []string{"bt", "55"}; len(ov.text) != 2 || ov.text[0] != want[0] || ov.text[1] != want[1] {
		t.Errorf("overlays = %v, want %v", ov.text, want)
	}
}

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
	case models.MediaPause:
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
	return models.MediaStatus{Playing: m.playing, Title: "Кино - Группа крови"}, nil
}

func (m *fakeMedia) set(playing bool) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.playing = playing
}

// takeOps waits for n operations and returns them.
func (m *fakeMedia) takeOps(t *testing.T, n int) []models.MediaOperation {
	t.Helper()
	deadline := time.Now().Add(time.Second)
	for {
		m.mu.Lock()
		if len(m.ops) >= n || time.Now().After(deadline) {
			ops := m.ops
			m.ops = nil
			m.mu.Unlock()
			return ops
		}
		m.mu.Unlock()
		time.Sleep(5 * time.Millisecond)
	}
}

func TestPanelMediaKeys(t *testing.T) {
	fifo := filepath.Join(t.TempDir(), "event0")
	if err := syscall.Mkfifo(fifo, 0o600); err != nil {
		t.Skip("mkfifo:", err)
	}
	vol := &fakeVolume{level: 50}
	media := &fakeMedia{playing: true}
	var busy atomic.Bool
	cfg := DefaultConfig()
	cfg.Device = fifo
	cfg.RepeatDelay = 80 * time.Millisecond
	cfg.RepeatInterval = time.Hour // one volume step per hold
	p := &Panel{
		Config:     cfg,
		Dispatcher: &controller.Dispatcher{Volume: vol, Media: media},
		Busy:       busy.Load,
	}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	out := make(chan wakeword.Detection, 4)
	go p.Run(ctx, nil, out)
	w, err := os.OpenFile(fifo, os.O_WRONLY, 0)
	if err != nil {
		t.Fatal(err)
	}
	defer w.Close()
	send := func(code uint16, v int32) { w.Write(event(code, v)) }
	tap := func(code uint16) { send(code, 1); send(code, 0) }
	expectOps := func(what string, want ...models.MediaOperation) {
		t.Helper()
		if got := media.takeOps(t, len(want)); !slices.Equal(got, want) {
			t.Errorf("%s: media operations = %v, want %v", what, got, want)
		}
	}
	expectWake := func(what string, want bool) {
		t.Helper()
		select {
		case <-out:
			if !want {
				t.Errorf("%s: the assistant was activated", what)
			}
		case <-time.After(100 * time.Millisecond):
			if want {
				t.Errorf("%s: the assistant was not activated", what)
			}
		}
	}

	// Taps on the volume keys skip tracks.
	tap(KeyVolumeUp)
	expectOps("tap volume up", models.MediaNext)
	tap(KeyVolumeDown)
	expectOps("tap volume down", models.MediaPrevious)

	// Holding changes the volume and does not skip.
	send(KeyVolumeUp, 1)
	time.Sleep(150 * time.Millisecond)
	send(KeyVolumeUp, 0)
	time.Sleep(50 * time.Millisecond)
	if lvl, _ := vol.Get(ctx); lvl.Level != 55 {
		t.Errorf("volume after a hold = %d, want 55", lvl.Level)
	}
	expectOps("hold volume up")

	// The centre key pauses, then plays on; no assistant.
	tap(KeyVoice)
	expectOps("centre while playing", models.MediaPause)
	expectWake("centre while playing", false)
	tap(KeyVoice)
	expectOps("centre after its pause", models.MediaPlay)
	expectWake("centre after its pause", false)

	// While the assistant is busy, the centre key is for it.
	busy.Store(true)
	tap(KeyVoice)
	expectWake("centre while busy", true)
	expectOps("centre while busy")
	busy.Store(false)

	// Music paused some other way: the centre key is the assistant again,
	// and the volume keys change the volume at once.
	media.set(false)
	tap(KeyVoice)
	expectWake("centre with paused music", true)
	expectOps("centre with paused music")
	tap(KeyVolumeDown)
	time.Sleep(50 * time.Millisecond)
	if lvl, _ := vol.Get(ctx); lvl.Level != 50 {
		t.Errorf("volume after a tap without music = %d, want 50", lvl.Level)
	}
	expectOps("tap volume down without music")
}
