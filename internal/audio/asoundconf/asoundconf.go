// Copyright 2026 The Marusya-Gemini Authors.
//
// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.
// You may obtain a copy of the License at
//
//     https://www.apache.org/licenses/LICENSE-2.0
//
// Unless required by applicable law or agreed to in writing, software
// distributed under the License is distributed on an "AS IS" BASIS,
// WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
// See the License for the specific language governing permissions and
// limitations under the License.

// Package asoundconf keeps the speaker's /etc/asound.conf: the ALSA setup
// of VK Капсула Мини with a LADSPA equalizer that voices its small driver.
// The installer puts [Template] there, the agent brings it up to date on
// every start and the web panel tunes the equalizer through [File].
//
// Only the block between the "# marusya-eq:" lines is kept across updates.
// It says whether the bands are the defaults, which then follow the
// template of the running agent, or were set by hand.
package asoundconf

import (
	"context"
	_ "embed"
	"errors"
	"fmt"
	"os"
	"strconv"
	"strings"
	"sync"

	"github.com/yawaflua/marusya-gemini/internal/models"
)

// Template is the asound.conf with the default equalizer.
//
//go:embed asound.conf
var Template string

// Marker starts the lines that delimit the equalizer block. A file
// without them is not managed.
const Marker = "# marusya-eq:"

const (
	stateDefault = "default"
	stateCustom  = "custom"
	stateEnd     = "end"
)

// ErrUnmanaged means the file has no equalizer block: it was written by
// hand and the agent leaves it alone.
var ErrUnmanaged = errors.New("asound.conf has no " + Marker + " block")

// block is the equalizer block of a file.
type block struct {
	lines      []string
	start, end int // indexes of the marker lines
	indent     string
	custom     bool
	bands      []models.EQBand
}

// parse finds the equalizer block of content.
func parse(content string) (*block, error) {
	b := &block{lines: strings.Split(content, "\n"), start: -1, end: -1}
	for i, l := range b.lines {
		t := strings.TrimSpace(l)
		state, ok := strings.CutPrefix(t, Marker)
		if !ok {
			continue
		}
		switch strings.TrimSpace(state) {
		case stateDefault, stateCustom:
			if b.start >= 0 {
				return nil, errors.New("asound.conf: two " + Marker + " blocks")
			}
			b.start, b.custom = i, strings.TrimSpace(state) == stateCustom
			b.indent = l[:len(l)-len(strings.TrimLeft(l, " \t"))]
		case stateEnd:
			if b.start >= 0 && b.end < 0 {
				b.end = i
			}
		}
	}
	if b.start < 0 || b.end < 0 {
		return nil, ErrUnmanaged
	}
	for _, l := range b.lines[b.start+1 : b.end] {
		f := strings.Fields(l)
		if len(f) != 4 {
			continue // "controls [" and "]"
		}
		var v [4]float64
		for i, s := range f {
			x, err := strconv.ParseFloat(s, 64)
			if err != nil {
				return nil, fmt.Errorf("asound.conf: band %q: %w", strings.TrimSpace(l), err)
			}
			v[i] = x
		}
		b.bands = append(b.bands, models.EQBand{Mode: int(v[0]), Freq: v[1], Q: v[2], Gain: v[3]})
	}
	if err := models.ValidateEQ(b.bands); err != nil {
		return nil, fmt.Errorf("asound.conf: %w", err)
	}
	return b, nil
}

// render returns content with the equalizer block replaced.
func (b *block) render(bands []models.EQBand, custom bool) string {
	state := stateDefault
	if custom {
		state = stateCustom
	}
	in := b.indent
	out := []string{in + Marker + " " + state, in + "controls ["}
	for _, x := range bands {
		out = append(out, fmt.Sprintf("%s    %-2d %-5s %-5s %s", in, x.Mode, num(x.Freq), num(x.Q), num(x.Gain)))
	}
	out = append(out, in+"]", in+Marker+" "+stateEnd)
	lines := append(append(append([]string{}, b.lines[:b.start]...), out...), b.lines[b.end+1:]...)
	return strings.Join(lines, "\n")
}

func num(v float64) string { return strconv.FormatFloat(v, 'g', -1, 64) }

var template = func() *block {
	b, err := parse(Template)
	if err != nil {
		panic(err)
	}
	return b
}()

// Defaults returns the bands of [Template].
func Defaults() []models.EQBand { return append([]models.EQBand(nil), template.bands...) }

// Render returns [Template] with bands; custom marks them as set by hand.
func Render(bands []models.EQBand, custom bool) string { return template.render(bands, custom) }

// File is an asound.conf on the speaker. It implements
// controller.Equalizer.
type File struct {
	path string
	mu   sync.Mutex
}

// New returns the file at path, usually /etc/asound.conf.
func New(path string) *File { return &File{path: path} }

// Sync rewrites a managed file from [Template]: hand-set bands stay,
// default ones become the defaults of this agent. It reports whether the
// file changed, and returns [ErrUnmanaged] for a file written by hand.
func (f *File) Sync() (bool, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	data, err := os.ReadFile(f.path)
	if err != nil {
		return false, err
	}
	cur, err := parse(string(data))
	if errors.Is(err, ErrUnmanaged) {
		return false, err
	}
	// A block broken by hand falls back to the defaults.
	want := Render(Defaults(), false)
	if err == nil && cur.custom {
		want = Render(cur.bands, true)
	}
	if want == string(data) {
		return false, nil
	}
	return true, f.write(want)
}

// EQ returns the equalizer of the file.
func (f *File) EQ(context.Context) (models.EQState, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	data, err := os.ReadFile(f.path)
	if err != nil {
		return models.EQState{}, err
	}
	b, err := parse(string(data))
	if err != nil {
		return models.EQState{}, err
	}
	return models.EQState{Bands: b.bands, Custom: b.custom, Defaults: Defaults()}, nil
}

// SetEQ sets the bands by hand. ALSA reads the file whenever a device is
// opened, so they apply to the next sound played.
func (f *File) SetEQ(_ context.Context, bands []models.EQBand) error {
	if err := models.ValidateEQ(bands); err != nil {
		return err
	}
	return f.replace(Render(bands, true))
}

// ResetEQ returns to the defaults, which follow agent updates again.
func (f *File) ResetEQ(context.Context) error {
	return f.replace(Render(Defaults(), false))
}

// replace writes content over a managed file.
func (f *File) replace(content string) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	data, err := os.ReadFile(f.path)
	if err != nil {
		return err
	}
	if _, err := parse(string(data)); errors.Is(err, ErrUnmanaged) {
		return err
	}
	return f.write(content)
}

// write overwrites the file in place: the speaker's rootfs may not allow
// renames, the same as for the installer.
func (f *File) write(content string) error {
	if err := os.WriteFile(f.path, []byte(content), 0o644); err != nil {
		return fmt.Errorf("write %s: %w", f.path, err)
	}
	return nil
}
