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

package server

import (
	"fmt"
	"strings"

	"github.com/yawaflua/marusya-gemini/internal/models"
	"github.com/yawaflua/marusya-gemini/internal/provider"
)

// mediaSources names the players of [models.MediaStatus.Source] for the
// model.
var mediaSources = map[string]string{
	"mpv":      "плеер колонки (mpv)",
	"stream":   "YouTube или радио через сервер",
	"airplay":  "AirPlay с телефона",
	"spotifyd": "Spotify (spotifyd)",
}

// mediaNote tells the model what the speaker played when the user started
// speaking, so "стоп" goes to that music and "что играет?" needs no tool.
// Empty when nothing is loaded or the device did not say (older agents).
func mediaNote(st *models.MediaStatus) string {
	if st == nil || !st.Active() {
		return ""
	}
	var b strings.Builder
	if st.Playing {
		b.WriteString("[Колонка: сейчас играет музыка")
	} else {
		b.WriteString("[Колонка: музыка на паузе")
	}
	if st.Title != "" {
		fmt.Fprintf(&b, " «%s»", st.Title)
	}
	if st.Source != "" {
		src := mediaSources[st.Source]
		if src == "" {
			src = st.Source
		}
		fmt.Fprintf(&b, ", источник: %s", src)
	}
	b.WriteString(". Это сведения от колонки, а не слова пользователя. Просьбы остановить, поставить на паузу, продолжить или переключить трек относятся к этой музыке; на вопрос, что играет, ответь по этой пометке, не вызывая инструментов.]")
	return b.String()
}

// noteTurn passes what the device reported at turn_start to the model, if
// the provider takes notes.
func (d *deviceSession) noteTurn(ps provider.Session, m *models.Message) {
	note := mediaNote(m.Media)
	if note == "" {
		return
	}
	tn, ok := ps.(provider.TurnNoter)
	if !ok {
		return
	}
	if err := tn.NoteTurn(note); err != nil {
		d.log.Warn("turn note", "err", err)
		return
	}
	d.log.Debug("turn note", "media", m.Media.Title, "playing", m.Media.Playing, "source", m.Media.Source)
}
