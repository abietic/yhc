package tui

import (
	"fmt"
	"math"
	"strconv"
	"time"

	tea "charm.land/bubbletea/v2"
	"charm.land/lipgloss/v2"
)

// spinnerTickMsg triggers a spinner frame update.
type spinnerTickMsg struct{}

// spinnerBreathTicks is one 960ms breathing cycle on the existing 120ms tick.
const spinnerBreathTicks = 8

// spinnerGlyph stays fixed while its semantic foreground breathes.
func spinnerGlyph() string {
	return assistantIdentityGlyph
}

// spinnerBreathIntensity returns a symmetric 0..1 intensity. Negative counters
// wrap safely and tick spinnerBreathTicks repeats tick zero.
func spinnerBreathIntensity(counter int) float64 {
	idx := counter % spinnerBreathTicks
	if idx < 0 {
		idx += spinnerBreathTicks
	}
	if idx > spinnerBreathTicks/2 {
		idx = spinnerBreathTicks - idx
	}
	return float64(idx) / float64(spinnerBreathTicks/2)
}

func styleForegroundString(style lipgloss.Style) string {
	return tuiColorString(style.GetForeground())
}

func truecolorRGB(color string) ([3]uint8, bool) {
	var rgb [3]uint8
	if len(color) != 7 || color[0] != '#' {
		return rgb, false
	}
	value, err := strconv.ParseUint(color[1:], 16, 24)
	if err != nil {
		return rgb, false
	}
	rgb[0] = uint8(value >> 16)
	rgb[1] = uint8(value >> 8)
	rgb[2] = uint8(value)
	return rgb, true
}

func interpolateRGB(base, peak [3]uint8, intensity float64) [3]uint8 {
	var color [3]uint8
	for i := range color {
		value := float64(base[i]) + float64(int(peak[i])-int(base[i]))*intensity
		color[i] = uint8(math.Round(value))
	}
	return color
}

// spinnerPulseStyle approximates opacity without painting a terminal
// background: truecolor palettes interpolate from the semantic subtle
// foreground to the caller's peak style, while ANSI palettes use those two
// semantic colors as a deterministic fallback.
func spinnerPulseStyle(subtle, peak lipgloss.Style, counter int) lipgloss.Style {
	intensity := spinnerBreathIntensity(counter)
	baseRGB, baseOK := truecolorRGB(styleForegroundString(subtle))
	peakRGB, peakOK := truecolorRGB(styleForegroundString(peak))
	if !baseOK || !peakOK {
		if intensity < 0.5 {
			return peak.Foreground(subtle.GetForeground())
		}
		return peak
	}
	color := interpolateRGB(baseRGB, peakRGB, intensity)
	return peak.Foreground(lipgloss.Color(fmt.Sprintf("#%02X%02X%02X", color[0], color[1], color[2])))
}

func (a *App) spinnerPulseIcon(peak lipgloss.Style, counter int) string {
	if a.reducedMotion {
		return peak.Render(spinnerGlyph())
	}
	return spinnerPulseStyle(a.styles.Subtle, peak, counter).Render(spinnerGlyph())
}

// spinnerTick returns a Cmd that sends a tick after a short delay.
// 120ms matches the reference frame duration (SpinnerAnimationRow.tsx:131).
func spinnerTick() tea.Cmd {
	return spinnerTickAfter(120 * time.Millisecond)
}

func spinnerTickAfter(delay time.Duration) tea.Cmd {
	return tea.Tick(delay, func(time.Time) tea.Msg {
		return spinnerTickMsg{}
	})
}

type scrollAnimTickMsg struct{}

func scrollAnimTick() tea.Cmd {
	return tea.Tick(50*time.Millisecond, func(time.Time) tea.Msg {
		return scrollAnimTickMsg{}
	})
}

// SpinnerMode represents the current phase of model activity.
type SpinnerMode int

const (
	SpinnerThinking   SpinnerMode = iota // Before first token
	SpinnerResponding                    // After first token, streaming
	SpinnerToolUse                       // During tool execution
)

// SpinnerState tracks contextual spinner information matching the reference
// SpinnerWithVerb component (verb text + elapsed time).
type SpinnerState struct {
	Mode          SpinnerMode
	StartTime     time.Time
	ToolName      string    // populated when Mode == SpinnerToolUse
	LastEventTime time.Time // last streaming event; zero means use StartTime
}

// stallThreshold is the duration after which spinner indicates inactivity.
const stallThreshold = 30 * time.Second

// RecordEvent updates the last event timestamp (call on each streaming delta or tool result).
func (s *SpinnerState) RecordEvent() {
	s.LastEventTime = time.Now()
}

// IsStalled returns true if no events have been received for stallThreshold.
func (s *SpinnerState) IsStalled() bool {
	ref := s.LastEventTime
	if ref.IsZero() {
		ref = s.StartTime
	}
	if ref.IsZero() {
		return false
	}
	return time.Since(ref) >= stallThreshold
}

// Text returns the contextual verb string for the current mode.
// Spinner verb rotation lists (reference: src/constants/spinnerVerbs.ts)
var (
	thinkingVerbs   = []string{"Thinking", "Analyzing", "Reasoning", "Considering", "Evaluating"}
	respondingVerbs = []string{"Responding", "Writing", "Composing", "Crafting", "Generating"}
)

func (s *SpinnerState) Text() string {
	switch s.Mode {
	case SpinnerThinking:
		idx := int(time.Since(s.StartTime).Seconds()/6) % len(thinkingVerbs)
		return thinkingVerbs[idx] + "…"
	case SpinnerResponding:
		idx := int(time.Since(s.StartTime).Seconds()/5) % len(respondingVerbs)
		return respondingVerbs[idx] + "…"
	case SpinnerToolUse:
		if s.ToolName != "" {
			return s.ToolName + "…"
		}
		return "Running tool…"
	}
	return "Working…"
}

// StaticText returns a non-rotating activity label for reduced-motion mode.
func (s *SpinnerState) StaticText() string {
	switch s.Mode {
	case SpinnerThinking:
		return "Thinking..."
	case SpinnerResponding:
		return "Responding..."
	case SpinnerToolUse:
		if s.ToolName != "" {
			return s.ToolName + "..."
		}
		return "Running tool..."
	default:
		return "Working..."
	}
}

// Duration returns a human-readable elapsed time string.
// Returns empty string if less than 1 second has passed.
func (s *SpinnerState) Duration() string {
	d := time.Since(s.StartTime)
	if d < time.Second {
		return ""
	}
	return fmt.Sprintf("%.1fs", d.Seconds())
}

// spinnerShimmerPeriod is the single 2.4s sine-shaped aurora shimmer cycle
// shared by every spinner mode (P19.2.2).
const spinnerShimmerPeriod = 2.4

// thinkingShimmerDelay keeps the existing 3s delay before shimmer starts in
// thinking mode (reference: THINKING_DELAY_MS = 3000).
const thinkingShimmerDelay = 3.0

// spinnerShimmerPhase maps an elapsed duration to a 0..1 sine-wave value on
// the shared 2.4s period. Thinking mode holds at 0 until thinkingShimmerDelay.
func spinnerShimmerPhase(elapsedSeconds float64, mode SpinnerMode) float64 {
	if mode == SpinnerThinking && elapsedSeconds < thinkingShimmerDelay {
		return 0 // no shimmer for first 3s of thinking
	}
	return (math.Sin(elapsedSeconds*math.Pi*2/spinnerShimmerPeriod) + 1) / 2
}

// ShimmerPhase returns a 0..1 sine-wave value for color interpolation.
// All modes share one 2.4s period on the existing 120ms tick; thinking keeps
// its 3s delay before shimmer starts.
func (s *SpinnerState) ShimmerPhase() float64 {
	return spinnerShimmerPhase(time.Since(s.StartTime).Seconds(), s.Mode)
}

// StallIntensity returns 0..1 representing how stalled the spinner is.
// Ramps linearly from 0 to 1 over 2 seconds after stallThreshold.
// Reference: useStalledAnimation.ts — linear ramp after 3s (we use 30s threshold).
func (s *SpinnerState) StallIntensity() float64 {
	ref := s.LastEventTime
	if ref.IsZero() {
		ref = s.StartTime
	}
	if ref.IsZero() {
		return 0
	}
	since := time.Since(ref)
	if since < stallThreshold {
		return 0
	}
	extra := (since - stallThreshold).Seconds()
	if extra > 2.0 {
		return 1.0
	}
	return extra / 2.0
}

// auroraShimmerColor returns the verb glimmer highlight for a 0..1 shimmer
// phase. Truecolor palettes interpolate three semantic stops — brand teal
// (AssistantPrefix) at 0, AuroraSky at 0.5, permission violet at 1. The
// Styles schema has no dedicated permission token yet, so the violet stop
// reads the DialogTitle foreground, which carries the palette permission
// color. ANSI palettes skip interpolation entirely and return the flat
// SpinnerShimmer semantic without constructing a truecolor value.
func auroraShimmerColor(styles Styles, phase float64) tuiColor {
	if phase < 0 {
		phase = 0
	}
	if phase > 1 {
		phase = 1
	}
	fallback := lipgloss.Color(styleForegroundString(styles.SpinnerShimmer))
	brandRGB, ok := truecolorRGB(styleForegroundString(styles.AssistantPrefix))
	if !ok {
		return fallback
	}
	skyRGB, ok := truecolorRGB(styleForegroundString(styles.AuroraSky))
	if !ok {
		return fallback
	}
	violetRGB, ok := truecolorRGB(styleForegroundString(styles.DialogTitle))
	if !ok {
		return fallback
	}
	var color [3]uint8
	if phase < 0.5 {
		color = interpolateRGB(brandRGB, skyRGB, phase*2)
	} else {
		color = interpolateRGB(skyRGB, violetRGB, (phase-0.5)*2)
	}
	return lipgloss.Color(fmt.Sprintf("#%02X%02X%02X", color[0], color[1], color[2]))
}

func renderAuroraShimmerText(text string, tick int, styles Styles, phase float64) string {
	baseColor := lipgloss.Color(styleForegroundString(styles.AssistantPrefix))
	return RenderShimmerText(text, tick, baseColor, auroraShimmerColor(styles, phase))
}

// computeGlimmerIndex computes which character position the glimmer highlight
// is at for the given tick and message width.
// Mirrors src/bridge/bridgeStatusUtil.ts computeGlimmerIndex.
// The glimmer sweeps from right to left across the text and wraps.
func computeGlimmerIndex(tick, messageWidth int) int {
	cycleLength := messageWidth + 20
	if cycleLength <= 0 {
		return -1
	}
	return messageWidth + 10 - (tick % cycleLength)
}

// ShimmerSegments holds the three segments of text split by the glimmer position.
type ShimmerSegments struct {
	Before  string
	Shimmer string
	After   string
}

// ComputeShimmerSegments splits text into before/shimmer/after segments
// based on a glimmer index. The shimmer is a 3-character-wide highlight.
// Mirrors src/bridge/bridgeStatusUtil.ts computeShimmerSegments.
func ComputeShimmerSegments(text string, glimmerIndex int) ShimmerSegments {
	runes := []rune(text)
	messageWidth := len(runes)
	shimmerStart := glimmerIndex - 1
	shimmerEnd := glimmerIndex + 1

	// When shimmer is offscreen, return all text as "before"
	if shimmerStart >= messageWidth || shimmerEnd < 0 {
		return ShimmerSegments{Before: text}
	}

	clampedStart := shimmerStart
	if clampedStart < 0 {
		clampedStart = 0
	}

	var before, shimmer, after []rune
	for i, r := range runes {
		if i < clampedStart {
			before = append(before, r)
		} else if i > shimmerEnd {
			after = append(after, r)
		} else {
			shimmer = append(shimmer, r)
		}
	}

	return ShimmerSegments{
		Before:  string(before),
		Shimmer: string(shimmer),
		After:   string(after),
	}
}

// RenderShimmerText renders text with a per-character glimmer highlight.
// The glimmer position is determined by the tick counter.
// messageColor is the base color, shimmerColor is the highlight.
func RenderShimmerText(text string, tick int, messageColor, shimmerColor tuiColor) string {
	runes := []rune(text)
	glimmerIdx := computeGlimmerIndex(tick, len(runes))
	segments := ComputeShimmerSegments(text, glimmerIdx)

	baseStyle := lipgloss.NewStyle().Foreground(messageColor)
	highlightStyle := lipgloss.NewStyle().Foreground(shimmerColor)

	result := ""
	if segments.Before != "" {
		result += baseStyle.Render(segments.Before)
	}
	if segments.Shimmer != "" {
		result += highlightStyle.Render(segments.Shimmer)
	}
	if segments.After != "" {
		result += baseStyle.Render(segments.After)
	}
	return result
}
