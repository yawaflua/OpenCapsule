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

package asoundconf

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"github.com/yawaflua/marusya-gemini/internal/models"
)

var ctx = context.Background()

func writeConf(t *testing.T, content string) (*File, string) {
	t.Helper()
	p := filepath.Join(t.TempDir(), "asound.conf")
	if err := os.WriteFile(p, []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
	return New(p), p
}

func read(t *testing.T, p string) string {
	t.Helper()
	b, err := os.ReadFile(p)
	if err != nil {
		t.Fatal(err)
	}
	return string(b)
}

var custom = []models.EQBand{
	{Mode: models.EQLowShelf, Freq: 150, Q: 0.5, Gain: -2},
	{Mode: models.EQPeak, Freq: 450, Q: 0.9, Gain: -7.5},
	{Mode: models.EQPeak, Freq: 1650, Q: 0.85, Gain: -6},
	{Mode: models.EQOff, Freq: 8000, Q: 0.25, Gain: 0},
}

func TestTemplateRoundTrips(t *testing.T) {
	if err := models.ValidateEQ(Defaults()); err != nil {
		t.Fatal(err)
	}
	// The defaults render back to the template byte for byte, so a
	// default file is left alone on every start.
	if got := Render(Defaults(), false); got != Template {
		t.Errorf("rendered template differs:\n%s", got)
	}
	// The block keeps the template's layout around it.
	r := Render(custom, true)
	for _, want := range []string{
		"            " + Marker + " custom\n            controls [\n",
		"                0  150   0.5   -2\n",
		"                1  450   0.9   -7.5\n",
		"                -1 8000  0.25  0\n            ]\n            " + Marker + " end\n        }\n",
		"pcm.!default {",
	} {
		if !strings.Contains(r, want) {
			t.Errorf("rendered file lacks %q:\n%s", want, r)
		}
	}
	b, err := parse(r)
	if err != nil || !b.custom || !slices.Equal(b.bands, custom) {
		t.Errorf("parse back: %+v, %v", b, err)
	}
}

func TestSetAndReset(t *testing.T) {
	f, p := writeConf(t, Template)
	st, err := f.EQ(ctx)
	if err != nil || st.Custom || !slices.Equal(st.Bands, Defaults()) || !slices.Equal(st.Defaults, Defaults()) {
		t.Fatalf("EQ: %+v, %v", st, err)
	}
	if err := f.SetEQ(ctx, custom); err != nil {
		t.Fatal(err)
	}
	if st, _ := f.EQ(ctx); !st.Custom || !slices.Equal(st.Bands, custom) {
		t.Errorf("after set: %+v", st)
	}
	bad := slices.Clone(custom)
	bad[1].Gain = 20
	if err := f.SetEQ(ctx, bad); err == nil {
		t.Error("a +20 dB band was accepted")
	}
	if err := f.SetEQ(ctx, custom[:3]); err == nil {
		t.Error("three bands were accepted")
	}
	if err := f.ResetEQ(ctx); err != nil {
		t.Fatal(err)
	}
	if got := read(t, p); got != Template {
		t.Errorf("reset file differs from the template:\n%s", got)
	}
}

// oldTemplate is a managed file written by an older agent: other
// defaults and other text around them.
func oldTemplate(state string) string {
	return "# old\n" + strings.Replace(Render([]models.EQBand{
		{Mode: 0, Freq: 140, Q: 0.5, Gain: -6},
		{Mode: 1, Freq: 320, Q: 0.4, Gain: -4},
		{Mode: 1, Freq: 3800, Q: 0.3, Gain: 3},
		{Mode: -1, Freq: 8000, Q: 0.25, Gain: 0},
	}, false), Marker+" default", Marker+" "+state, 1)
}

func TestSyncUpdatesDefaults(t *testing.T) {
	f, p := writeConf(t, oldTemplate("default"))
	if changed, err := f.Sync(); err != nil || !changed {
		t.Fatalf("Sync: %v, %v", changed, err)
	}
	if got := read(t, p); got != Template {
		t.Errorf("default bands not updated:\n%s", got)
	}
	if changed, err := f.Sync(); err != nil || changed {
		t.Errorf("second Sync: %v, %v", changed, err)
	}
}

func TestSyncKeepsCustomBands(t *testing.T) {
	f, p := writeConf(t, oldTemplate("custom"))
	if _, err := f.Sync(); err != nil {
		t.Fatal(err)
	}
	got := read(t, p)
	if strings.HasPrefix(got, "# old") {
		t.Error("the text around the block was not updated")
	}
	b, err := parse(got)
	if err != nil || !b.custom || b.bands[1].Freq != 320 {
		t.Errorf("custom bands lost: %+v, %v\n%s", b, err, got)
	}
}

func TestSyncRepairsBrokenBlock(t *testing.T) {
	f, p := writeConf(t, strings.Replace(Template, "1  460   0.9   -6", "1  460   x   -6", 1))
	if _, err := f.Sync(); err != nil {
		t.Fatal(err)
	}
	if got := read(t, p); got != Template {
		t.Errorf("broken block not replaced with the defaults:\n%s", got)
	}
}

func TestUnmanagedFileIsLeftAlone(t *testing.T) {
	const own = "pcm.!default { type hw card 0 }\n"
	f, p := writeConf(t, own)
	if _, err := f.Sync(); !errors.Is(err, ErrUnmanaged) {
		t.Errorf("Sync: %v", err)
	}
	if _, err := f.EQ(ctx); !errors.Is(err, ErrUnmanaged) {
		t.Errorf("EQ: %v", err)
	}
	if err := f.SetEQ(ctx, custom); !errors.Is(err, ErrUnmanaged) {
		t.Errorf("SetEQ: %v", err)
	}
	if err := f.ResetEQ(ctx); !errors.Is(err, ErrUnmanaged) {
		t.Errorf("ResetEQ: %v", err)
	}
	if got := read(t, p); got != own {
		t.Errorf("file changed:\n%s", got)
	}

	if _, err := New(filepath.Join(t.TempDir(), "none")).Sync(); !errors.Is(err, os.ErrNotExist) {
		t.Errorf("missing file: %v", err)
	}
}
