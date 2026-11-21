package cmd

import (
	"os"
	"path/filepath"
	"testing"
)

func TestDesktopSpeechConfigRequiresOptInAndPricedBudget(t *testing.T) {
	env := map[string]string{}
	getenv := func(name string) string { return env[name] }
	config, err := desktopSpeechConfig(getenv)
	if err != nil || config.Transcribe != nil || config.ReadAloud != nil {
		t.Fatal("speech must be disabled by default")
	}
	dir := t.TempDir()
	card := filepath.Join(dir, "prices.json")
	if err := os.WriteFile(card, []byte(`{
		"version":1, "currency":"CNY", "source":"https://api-docs.deepseek.com/zh-cn/quick_start/pricing/",
		"as_of":"2026-11-21T00:00:00Z", "window":"upper_bound",
		"routes":[{
			"provider":"agenticdeepseek", "model":"asr", "api_model":"asr",
			"cached_input_per_million":"0", "uncached_input_per_million":"0", "output_per_million":"0",
			"metered":{"unit":"audio_second", "amount":"1", "per":3600}
		}]
	}`), 0o600); err != nil {
		t.Fatal(err)
	}
	env["YHC_DESKTOP_RECORD_DRAFT"] = "1"
	env["YHC_DESKTOP_SPEECH_PRICE_CARD"] = card
	env["YHC_DESKTOP_SPEECH_API_KEY"] = "fixture-key"
	if config, err := desktopSpeechConfig(getenv); err == nil || config.Transcribe != nil {
		t.Fatal("enabled speech without a cost limit")
	}
	env["YHC_DESKTOP_SPEECH_MAX_COST"] = "1"
	config, err = desktopSpeechConfig(getenv)
	if err != nil || config.Transcribe == nil || config.ReadAloud != nil {
		t.Fatalf("could not configure transcription independently: %v", err)
	}
	env["YHC_DESKTOP_READ_ALOUD"] = "1"
	if config, err := desktopSpeechConfig(getenv); err == nil || config.ReadAloud != nil {
		t.Fatal("enabled read aloud without a billing route")
	}
}
