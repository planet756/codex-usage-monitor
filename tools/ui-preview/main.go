// Development-only layout preview. The Windows executable never imports this
// command, its HTML, a browser, or its sample dataset.
package main

import (
	"bytes"
	_ "embed"
	"encoding/json"
	"fmt"
	"html/template"
	"os"
	"path/filepath"

	"codex-usage-monitor/internal/i18n"
	"codex-usage-monitor/internal/layout"
	"codex-usage-monitor/internal/metrics"
	"codex-usage-monitor/internal/presentation"
)

//go:embed preview.html
var page string

type view struct {
	Strip           [3]presentation.Metric
	Counts          string
	CompactCounts   string
	Coverage        string
	Identity        string
	Rows            []presentation.DetailRow
	Labels          map[string]string
	Choices         presentation.StripChoices
	CountChoices    []string
	RowChoices      [][]presentation.DetailRow
	CoverageChoices []string
}

func main() {
	if len(os.Args) != 2 {
		panic("usage: go run ./tools/ui-preview /tmp/preview/index.html")
	}
	ttft := 1000.0
	s := metrics.Snapshot{Turns: 1, Steps: 5, HasCounts: true, CountsComplete: true, HasTiming: true, HistoryComplete: true, TimingComplete: true,
		ModelMS: 32200, ToolMS: 11000, AverageTTFT: &ttft, DecodeTokens: 5629, DecodeMS: 27062, DecodeSamples: 5, TPSApproximate: true}
	s = s.WithUsage(metrics.Usage{Input: 98481, Cached: 72704, Output: 5629}, true)
	rate := float64(s.DecodeTokens) * 1000 / float64(s.DecodeMS)
	s.TPS = &rate
	zero := metrics.Snapshot{HasCounts: true, CountsComplete: true, HistoryComplete: true, TimingComplete: true}.WithUsage(metrics.Usage{}, true)
	partial := s
	partial.HistoryComplete, partial.CountsComplete = false, false
	skipped := s
	skipped.DecodeSamples, skipped.DecodeSkipped = 4, 1
	skipped.DecodeTokens, skipped.DecodeMS = 5500, 26000
	skippedRate := float64(skipped.DecodeTokens) * 1000 / float64(skipped.DecodeMS)
	skipped.TPS = &skippedRate
	large := s.WithUsage(metrics.Usage{Input: 987654321, Cached: 876543210, Output: 123456789}, true)
	large.Turns, large.Steps = 123456, 999999
	caption := s.WithUsage(metrics.Usage{Input: 80000000, Cached: 78400000, Output: 600000}, true)
	caption.Turns, caption.Steps = 12, 133
	caption.DecodeTokens, caption.DecodeMS, caption.DecodeSamples = 600000, 16853933, 133
	caption.ModelMS = 16860000
	captionRate := float64(caption.DecodeTokens) * 1000 / float64(caption.DecodeMS)
	caption.TPS = &captionRate
	states := map[string]metrics.Snapshot{"sample": s, "caption": caption, "skipped": skipped, "zero": zero, "missing": {}, "partial": partial, "large": large, "loading": {},
		"new_chat_pending": {}, "new_chat_connected": {}}
	messages := map[string]string{"new_chat_pending": "Waiting for current chat identity", "new_chat_connected": "New chat · waiting for chat ID"}
	for _, tc := range []struct {
		name                        string
		turns, steps, input, output int64
		rate                        float64
		complete                    bool
	}{
		{"billions", 759, 16501, 3900000000, 15000000, 34.1, true},
		{"partial_large", 123456, 1999999, 4000000000, 123456789, 208.2, false},
		{"hundred_percent", 999999, 999999999, 99999999999, 9999999999, 999.9, true},
		{"trillions", 9999999, 999999999, 1000000000000, 10000000000, 1234.5, true},
		{"int64_limit", 9223372036854775807, 9223372036854775807, 9223372036854775807, 0, 9.223372036854776e21, true},
		{"partial_int64", 9223372036854775807, 9223372036854775807, 9223372036854775807, 0, 9.223372036854776e21, false},
	} {
		rate := tc.rate
		x := metrics.Snapshot{HasCounts: true, CountsComplete: tc.complete, HistoryComplete: tc.complete, HasTiming: true, Turns: tc.turns, Steps: tc.steps, TPS: &rate, TPSApproximate: true, DecodeSamples: tc.steps}
		states[tc.name] = x.WithUsage(metrics.Usage{Input: tc.input, Cached: tc.input, Output: tc.output}, true)
	}
	views := map[string]map[string]view{}
	for _, lang := range []string{"en", "zh-CN"} {
		views[lang] = map[string]view{}
		for state, snapshot := range states {
			labels := map[string]string{}
			for _, key := range []string{"Current chat usage", "Settings", "Language", "Appearance", "Automatic", "Light", "Dark", "Done", "Saved", "Changes apply immediately.", "Loading chat history…", "Waiting for data", "Quit", "cache"} {
				labels[key] = i18n.Text(lang, key)
			}
			id := "8d9e21ab-4170-4e60-a658-0b218f025a31"
			if messages[state] != "" {
				id = ""
			}
			model := presentation.Display(snapshot, lang, id, messages[state], state == "loading", false, true)
			rows := model.Rows[:]
			coverage := presentation.CoverageAlternatives(snapshot, lang)
			if messages[state] != "" {
				coverage = []string{i18n.Text(lang, model.Coverage)}
			}
			var rowChoices [][]presentation.DetailRow
			for _, row := range rows {
				rowChoices = append(rowChoices, presentation.DetailAlternatives(row, lang))
			}
			views[lang][state] = view{Strip: model.Parts, Counts: model.Counts,
				CompactCounts: presentation.CompactCounts(snapshot, lang),
				Coverage:      i18n.Text(lang, model.Coverage), Identity: model.Identity,
				Rows: rows, Labels: labels, Choices: presentation.StripAlternatives(snapshot, lang), CountChoices: presentation.CountAlternatives(snapshot, lang),
				RowChoices: rowChoices, CoverageChoices: coverage}
		}
	}
	dataset := map[string]any{"views": views, "light": presentation.Colors(false), "dark": presentation.Colors(true),
		"layout": map[string]any{"rows": layout.DetailRows, "height": layout.DetailHeight, "rule": layout.DetailRuleY, "footer": layout.DetailFooterY,
			"minWidth": layout.StripWidth, "maxWidth": layout.StripMaxWidth, "widthStep": layout.StripWidthStep}}
	b, err := json.Marshal(dataset)
	if err != nil {
		panic(err)
	}
	var out bytes.Buffer
	if err = template.Must(template.New("preview").Parse(page)).Execute(&out, template.JS(b)); err != nil {
		panic(err)
	}
	path := os.Args[1]
	if err = os.MkdirAll(filepath.Dir(path), 0700); err != nil {
		panic(err)
	}
	if err = os.WriteFile(path, out.Bytes(), 0600); err != nil {
		panic(err)
	}
	fmt.Println(path)
}
