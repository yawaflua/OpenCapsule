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

package models

import (
	"encoding/json"
	"errors"
	"fmt"
	"math"
)

const (
	// ActionEQGet returns the speaker's [EQState].
	ActionEQGet Action = "eq.get"
	// ActionEQSet replaces the speaker's equalizer. Args: "bands", a list
	// of [EQBandCount] objects {mode, freq, q, gain} (see [EQBand]).
	ActionEQSet Action = "eq.set"
	// ActionEQReset returns the equalizer to the defaults of the running
	// agent, which then follow agent updates again.
	ActionEQReset Action = "eq.reset"
)

// EQBandCount is the number of bands of the speaker's equalizer (the
// four of the CAPS Eq4p LADSPA plugin).
const EQBandCount = 4

// EQ band modes, as the Eq4p plugin numbers them.
const (
	EQOff       = -1
	EQLowShelf  = 0
	EQPeak      = 1
	EQHighShelf = 2
)

// Limits of an [EQBand]. Freq and Q are the plugin's own ranges; gain is
// narrower than the plugin allows: a small driver does not survive +24 dB.
const (
	EQMinFreq = 20
	EQMaxFreq = 14000
	EQMinGain = -24
	EQMaxGain = 12
)

// EQBand is one band of the speaker's equalizer.
type EQBand struct {
	// Mode is one of [EQOff], [EQLowShelf], [EQPeak] and [EQHighShelf].
	Mode int `json:"mode"`
	// Freq is the centre or corner frequency in Hz.
	Freq float64 `json:"freq"`
	// Q is the plugin's 0..1 bandwidth knob: higher is narrower.
	Q float64 `json:"q"`
	// Gain is in dB.
	Gain float64 `json:"gain"`
}

// Validate checks that b is within what the plugin and the speaker take.
func (b EQBand) Validate() error {
	switch {
	case b.Mode < EQOff || b.Mode > EQHighShelf:
		return fmt.Errorf("mode %d out of range %d..%d", b.Mode, EQOff, EQHighShelf)
	case !inRange(b.Freq, EQMinFreq, EQMaxFreq):
		return fmt.Errorf("frequency %g Hz out of range %d..%d", b.Freq, EQMinFreq, EQMaxFreq)
	case !inRange(b.Q, 0, 1):
		return fmt.Errorf("Q %g out of range 0..1", b.Q)
	case !inRange(b.Gain, EQMinGain, EQMaxGain):
		return fmt.Errorf("gain %g dB out of range %d..%d", b.Gain, EQMinGain, EQMaxGain)
	}
	return nil
}

func inRange(v, lo, hi float64) bool {
	return !math.IsNaN(v) && v >= lo && v <= hi
}

// ValidateEQ checks a complete set of bands.
func ValidateEQ(bands []EQBand) error {
	if len(bands) != EQBandCount {
		return fmt.Errorf("want %d bands, got %d", EQBandCount, len(bands))
	}
	for i, b := range bands {
		if err := b.Validate(); err != nil {
			return fmt.Errorf("band %d: %w", i+1, err)
		}
	}
	return nil
}

// EQState is the speaker's equalizer.
type EQState struct {
	Bands []EQBand `json:"bands"`
	// Custom is set once the bands were changed by hand; otherwise they are
	// the agent's defaults and change with agent updates.
	Custom bool `json:"custom"`
	// Defaults are the bands [ActionEQReset] restores.
	Defaults []EQBand `json:"defaults"`
}

// Map converts the state into a generic result payload.
func (s EQState) Map() map[string]any {
	return map[string]any{"bands": s.Bands, "custom": s.Custom, "defaults": s.Defaults}
}

// EQBands returns the named list of bands, checked with [ValidateEQ].
//
// The argument arrives as decoded JSON ([]any of map[string]any), so it is
// converted through JSON.
func (c *Command) EQBands(key string) ([]EQBand, error) {
	v, ok := c.Args[key]
	if !ok {
		return nil, errors.New("missing argument: " + key)
	}
	raw, err := json.Marshal(v)
	if err != nil {
		return nil, err
	}
	var bands []EQBand
	if err := json.Unmarshal(raw, &bands); err != nil {
		return nil, fmt.Errorf("%s: %w", key, err)
	}
	if err := ValidateEQ(bands); err != nil {
		return nil, err
	}
	return bands, nil
}
