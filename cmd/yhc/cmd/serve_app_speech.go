package cmd

import (
	"context"
	"fmt"
	"os"
	"strings"

	"github.com/abietic/yhc/engine/execution"
	"github.com/abietic/yhc/engine/provider"
	"github.com/abietic/yhc/server/appserver"
)

func desktopSpeechConfig(getenv func(string) string) (appserver.SpeechConfig, error) {
	var speech appserver.SpeechConfig
	record := getenv("YHC_DESKTOP_RECORD_DRAFT") == "1"
	readAloud := getenv("YHC_DESKTOP_READ_ALOUD") == "1"
	if !record && !readAloud {
		return speech, nil
	}
	priceFile, err := os.Open(getenv("YHC_DESKTOP_SPEECH_PRICE_CARD"))
	if err != nil {
		return speech, fmt.Errorf("set YHC_DESKTOP_SPEECH_PRICE_CARD to a readable price card")
	}
	defer priceFile.Close() //nolint:errcheck
	card, err := execution.DecodeRunPriceCard(priceFile)
	if err != nil {
		return speech, fmt.Errorf("invalid Desktop speech price card")
	}
	billingUnits := make(map[string]execution.BillingUnit, len(card.Routes))
	for _, route := range card.Routes {
		if route.Provider == string(provider.ProviderAgenticDeepSeek) && route.Model == route.APIModel && route.Metered != nil {
			billingUnits[route.Model] = route.Metered.Unit
		}
	}
	if (record && billingUnits["asr"] != execution.BillingAudioSecond) ||
		(readAloud && billingUnits["tts"] != execution.BillingCharacter) {
		return speech, fmt.Errorf("price card is missing the billing route for an enabled Desktop speech action")
	}
	usage, err := execution.NewPricedRunUsage(execution.RunUsageLimits{
		MaxEstimatedCost:          strings.TrimSpace(getenv("YHC_DESKTOP_SPEECH_MAX_COST")),
		ReserveCostBeforeDispatch: true,
	}, card)
	if err != nil {
		return speech, fmt.Errorf("set a positive YHC_DESKTOP_SPEECH_MAX_COST and use an upper_bound standalone price card")
	}

	key := strings.TrimSpace(getenv("YHC_DESKTOP_SPEECH_API_KEY"))
	if key == "" {
		key = getenv("DEEPSEEK_API_KEY")
	}
	client, err := provider.NewDeepSeekMedia(provider.DeepSeekMediaConfig{APIKey: key, Usage: usage})
	if err != nil {
		return speech, fmt.Errorf("set a valid YHC_DESKTOP_SPEECH_API_KEY or DEEPSEEK_API_KEY")
	}
	if record {
		speech.Transcribe = func(ctx context.Context, wav []byte) (string, error) {
			transcription, err := client.Transcribe(ctx, wav)
			return transcription.Text, err
		}
	}
	if readAloud {
		speech.ReadAloud = func(ctx context.Context, text string) ([]byte, error) {
			return client.Synthesize(ctx, provider.DeepSeekReadAloud{Text: text})
		}
	}
	return speech, nil
}
