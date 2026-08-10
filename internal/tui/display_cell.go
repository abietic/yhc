package tui

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"strings"
	"unicode"
	"unicode/utf8"

	"github.com/clipperhouse/displaywidth"
)

const displayCellIdentityVersion = "display-cell/v1"

type displayCellPolicy struct {
	UnicodeVersion            string `json:"unicode_version"`
	SegmentationMethod        string `json:"segmentation_method"`
	WidthMethod               string `json:"width_method"`
	AmbiguousWidth            string `json:"ambiguous_width"`
	EmojiPresentation         string `json:"emoji_presentation"`
	TabStop                   int    `json:"tab_stop"`
	ControlSequences7Bit      bool   `json:"control_sequences_7_bit"`
	ControlSequences8Bit      bool   `json:"control_sequences_8_bit"`
	IndicConjunctCells        int    `json:"indic_conjunct_cells"`
	LoneRegionalIndicatorCell int    `json:"lone_regional_indicator_cells"`
	PairedFlagCells           int    `json:"paired_flag_cells"`
	BareLabelCells            int    `json:"bare_label_cells"`
	UnsafeControlPolicy       string `json:"unsafe_control_policy"`
}

func defaultDisplayCellPolicy() displayCellPolicy {
	return displayCellPolicy{
		UnicodeVersion:            "17.0.0",
		SegmentationMethod:        "UAX #29 via uax29/v2 v2.7.0",
		WidthMethod:               "displaywidth v0.11.0",
		AmbiguousWidth:            "narrow",
		EmojiPresentation:         "Unicode TR51",
		TabStop:                   4,
		ControlSequences7Bit:      true,
		ControlSequences8Bit:      false,
		IndicConjunctCells:        2,
		LoneRegionalIndicatorCell: 1,
		PairedFlagCells:           2,
		BareLabelCells:            2,
		UnsafeControlPolicy:       "sanitize before layout; kernel replacement fallback",
	}
}

// DisplayCellProfile is the immutable display-cell policy selected for one
// App lifetime. Its unexported value state prevents callers from changing the
// selected grid after construction.
type DisplayCellProfile struct {
	id      string
	policy  displayCellPolicy
	options displaywidth.Options
}

var defaultDisplayCellProfileValue = newDisplayCellProfile(
	defaultDisplayCellPolicy(),
)

// DefaultDisplayCellProfile returns the deterministic project grid.
func DefaultDisplayCellProfile() DisplayCellProfile {
	return defaultDisplayCellProfileValue
}

func newDisplayCellProfile(policy displayCellPolicy) DisplayCellProfile {
	if !validDisplayCellPolicy(policy) {
		return DisplayCellProfile{}
	}
	return DisplayCellProfile{
		id:     deriveDisplayCellProfileIdentity(policy),
		policy: policy,
		options: displaywidth.Options{
			EastAsianWidth:       policy.AmbiguousWidth == "wide",
			ControlSequences:     policy.ControlSequences7Bit,
			ControlSequences8Bit: policy.ControlSequences8Bit,
		},
	}
}

func validDisplayCellPolicy(policy displayCellPolicy) bool {
	return policy.UnicodeVersion != "" &&
		policy.SegmentationMethod != "" &&
		policy.WidthMethod != "" &&
		(policy.AmbiguousWidth == "narrow" || policy.AmbiguousWidth == "wide") &&
		policy.EmojiPresentation != "" &&
		policy.TabStop > 0 &&
		policy.IndicConjunctCells > 0 &&
		policy.LoneRegionalIndicatorCell > 0 &&
		policy.PairedFlagCells > 0 &&
		policy.BareLabelCells > 0 &&
		policy.UnsafeControlPolicy != ""
}

func deriveDisplayCellProfileIdentity(policy displayCellPolicy) string {
	encoded, err := json.Marshal(policy)
	if err != nil {
		panic(fmt.Sprintf("encode display-cell policy: %v", err))
	}
	sum := sha256.Sum256(encoded)
	return displayCellIdentityVersion + "/sha256:" + hex.EncodeToString(sum[:])
}

func (p DisplayCellProfile) valid() bool {
	if !validDisplayCellPolicy(p.policy) ||
		p.id != deriveDisplayCellProfileIdentity(p.policy) {
		return false
	}
	return p.options.EastAsianWidth == (p.policy.AmbiguousWidth == "wide") &&
		p.options.ControlSequences == p.policy.ControlSequences7Bit &&
		p.options.ControlSequences8Bit == p.policy.ControlSequences8Bit
}

// Identity returns the exact immutable cache identity for this profile.
func (p DisplayCellProfile) Identity() string {
	return p.id
}

func (p DisplayCellProfile) diagnosticSummary() string {
	return strings.Join([]string{
		"Display Cell Profile",
		fmt.Sprintf("  Identity:        %s", p.id),
		fmt.Sprintf("  Unicode:         %s", p.policy.UnicodeVersion),
		fmt.Sprintf(
			"  Segmentation:    %s; width=%s",
			p.policy.SegmentationMethod,
			p.policy.WidthMethod,
		),
		fmt.Sprintf("  Ambiguous width: %s", p.policy.AmbiguousWidth),
		fmt.Sprintf("  Emoji policy:    %s", p.policy.EmojiPresentation),
		fmt.Sprintf(
			"  Tabs:            %d-cell stops; rectangle-origin aware",
			p.policy.TabStop,
		),
		fmt.Sprintf(
			"  Controls:        ANSI-7=%t; ANSI-8=%t; unsafe=%s",
			p.policy.ControlSequences7Bit,
			p.policy.ControlSequences8Bit,
			p.policy.UnsafeControlPolicy,
		),
		"  Terminal/font:   not inferred; separately collected evidence required",
	}, "\n")
}

type displayCellPresentation struct {
	bold, italic, code, strike bool
	image                      bool
	linkURL                    string
}

type displayCellRun struct {
	text         string
	presentation displayCellPresentation
}

// measuredDisplayCellCluster keeps canonical visible bytes separate from the
// terminal projection. They differ only when a tab expands into spaces.
type measuredDisplayCellCluster struct {
	source                 string
	text                   string
	cells                  int
	startColumn, endColumn int
	presentation           displayCellPresentation
	control                bool
}

type displayCellRunRange struct {
	start, end   int
	presentation displayCellPresentation
}

func (p DisplayCellProfile) width(s string) int {
	return p.measure(s, 0)
}

func (p DisplayCellProfile) measure(s string, startColumn int) int {
	column := maxInt(startColumn, 0)
	width := 0
	iter := p.options.StringGraphemes(s)
	for iter.Next() {
		cluster := iter.Value()
		_, cells, _ := p.projectCluster(cluster, iter.Width(), column)
		width += cells
		if cluster == "\n" {
			column = maxInt(startColumn, 0)
		} else {
			column += cells
		}
	}
	return width
}

func (p DisplayCellProfile) clusters(
	s string,
	startColumn int,
) []measuredDisplayCellCluster {
	column := maxInt(startColumn, 0)
	iter := p.options.StringGraphemes(s)
	var clusters []measuredDisplayCellCluster
	for iter.Next() {
		source := iter.Value()
		text, cells, control := p.projectCluster(
			source,
			iter.Width(),
			column,
		)
		clusters = append(clusters, measuredDisplayCellCluster{
			source:      source,
			text:        text,
			cells:       cells,
			startColumn: column,
			endColumn:   column + cells,
			control:     control,
		})
		if source == "\n" {
			column = maxInt(startColumn, 0)
		} else {
			column += cells
		}
	}
	return clusters
}

func (p DisplayCellProfile) projectCluster(
	source string,
	measured int,
	column int,
) (string, int, bool) {
	if source == "\t" {
		cells := p.tabCells(column)
		return strings.Repeat(" ", cells), cells, false
	}
	if isSupportedDisplayCellControl(source, measured) {
		return source, 0, true
	}
	text := strings.ToValidUTF8(source, string(unicode.ReplacementChar))
	text = strings.Map(func(r rune) rune {
		if (unicode.IsControl(r) && source != "\n" && source != "\t") ||
			isUnsafeDisplayCellRune(r) {
			return unicode.ReplacementChar
		}
		return r
	}, text)
	if text == source {
		return text, p.clusterWidth(source, measured), false
	}
	iter := p.options.StringGraphemes(text)
	width := 0
	for iter.Next() {
		width += p.clusterWidth(iter.Value(), iter.Width())
	}
	return text, width, false
}

func isSupportedDisplayCellControl(source string, measured int) bool {
	return isANSIControlCluster(source, measured) &&
		(isSGRSequence(source) || isOSC8Sequence(source))
}

func isUnsafeDisplayCellRune(r rune) bool {
	if r == '\n' || r == '\t' {
		return false
	}
	if unicode.IsControl(r) {
		return true
	}
	switch {
	case r == '\u061c' || r == '\u200e' || r == '\u200f':
		return true
	case r >= '\u202a' && r <= '\u202e':
		return true
	case r >= '\u2066' && r <= '\u2069':
		return true
	default:
		return false
	}
}

func (p DisplayCellProfile) measureRuns(
	runs []displayCellRun,
	startColumn int,
) []measuredDisplayCellCluster {
	var visible strings.Builder
	ranges := make([]displayCellRunRange, 0, len(runs))
	for _, run := range runs {
		if run.text == "" {
			continue
		}
		start := visible.Len()
		visible.WriteString(run.text)
		ranges = append(ranges, displayCellRunRange{
			start:        start,
			end:          visible.Len(),
			presentation: run.presentation,
		})
	}
	clusters := p.clusters(visible.String(), startColumn)
	offset := 0
	for index := range clusters {
		cluster := &clusters[index]
		ownerOffset := offset + firstVisibleScalarOffset(cluster.source)
		for _, runRange := range ranges {
			if ownerOffset >= runRange.start && ownerOffset < runRange.end {
				cluster.presentation = runRange.presentation
				break
			}
		}
		offset += len(cluster.source)
	}
	return clusters
}

func firstVisibleScalarOffset(cluster string) int {
	fallback := 0
	for index, r := range cluster {
		if index == 0 {
			fallback = index
		}
		if unicode.IsControl(r) || r == '\u200d' ||
			r == '\ufe0e' || r == '\ufe0f' ||
			unicode.Is(unicode.Mn, r) ||
			unicode.Is(unicode.Me, r) {
			continue
		}
		return index
	}
	return fallback
}

func (p DisplayCellProfile) tabCells(column int) int {
	column = maxInt(column, 0)
	return p.policy.TabStop - column%p.policy.TabStop
}

func (p DisplayCellProfile) expandTabs(s string, startColumn int) string {
	var result strings.Builder
	column := maxInt(startColumn, 0)
	iter := p.options.StringGraphemes(s)
	for iter.Next() {
		cluster := iter.Value()
		text, cells, _ := p.projectCluster(cluster, iter.Width(), column)
		result.WriteString(text)
		switch cluster {
		case "\n":
			column = maxInt(startColumn, 0)
		default:
			column += cells
		}
	}
	return result.String()
}

func (p DisplayCellProfile) truncate(s string, width int) string {
	return p.truncateAt(s, width, 0)
}

func (p DisplayCellProfile) truncateAt(
	s string,
	width int,
	startColumn int,
) string {
	if width < 0 {
		return ""
	}
	s = p.expandTabs(s, startColumn)
	if p.measure(s, startColumn) <= width {
		return s
	}

	var result strings.Builder
	iter := p.options.StringGraphemes(s)
	used := 0
	truncated := false
	for iter.Next() {
		cluster := iter.Value()
		clusterWidth := p.clusterWidth(cluster, iter.Width())
		if !truncated && used+clusterWidth <= width {
			result.WriteString(cluster)
			used += clusterWidth
			continue
		}
		truncated = true
		if isANSIControlCluster(cluster, clusterWidth) &&
			p.options.String(cluster) == 0 {
			// Keep trailing resets and OSC terminators so truncation cannot
			// leak style or hyperlink state into the following table cell.
			// Re-measure the sequence on its own: a zero-width fragment in
			// parser context can become a visible incomplete escape after
			// surrounding omitted bytes are removed.
			result.WriteString(cluster)
		}
	}
	return p.balanceControlLines([]string{result.String()})[0]
}

// wrap returns display-cell bounded lines without splitting an extended
// grapheme cluster or discarding ANSI SGR/OSC bytes. Word wrapping prefers the
// last ASCII whitespace that fits; hard wrapping uses the same cluster walk.
func (p DisplayCellProfile) wrap(s string, width int, hardWrap bool) []string {
	return p.wrapAt(s, width, hardWrap, 0)
}

func (p DisplayCellProfile) wrapAt(
	s string,
	width int,
	hardWrap bool,
	startColumn int,
) []string {
	s = p.expandTabs(s, startColumn)
	if width <= 0 {
		return p.balanceControlLines([]string{s})
	}
	if !strings.Contains(s, "\n") && p.measure(s, startColumn) <= width {
		return p.balanceControlLines([]string{s})
	}

	var lines []string
	for len(s) > 0 {
		line, rest := p.wrapLine(s, width, hardWrap, startColumn)
		lines = append(lines, line)
		if rest == s { // defensive progress guard for malformed input.
			break
		}
		s = rest
	}
	if len(lines) == 0 {
		return []string{""}
	}
	return p.balanceControlLines(lines)
}

func (p DisplayCellProfile) wrapLine(
	s string,
	width int,
	hardWrap bool,
	startColumn int,
) (string, string) {
	it := p.options.StringGraphemes(s)
	used, end := 0, 0
	lastSpaceEnd := -1
	column := maxInt(startColumn, 0)

	for it.Next() {
		cluster := it.Value()
		clusterWidth := p.clusterWidth(cluster, it.Width())
		if cluster == "\t" {
			clusterWidth = p.tabCells(column + used)
		}
		if cluster == "\n" {
			return s[:end], s[end+len(cluster):]
		}
		if used+clusterWidth > width {
			if !hardWrap && lastSpaceEnd >= 0 {
				return strings.TrimRight(s[:lastSpaceEnd], " \t"), strings.TrimLeft(s[lastSpaceEnd:], " \t")
			}
			if used == 0 {
				// A single wide cluster cannot fit. Keep it intact so callers
				// never emit an invalid partial grapheme. Zero-width control
				// prefixes remain attached to that first visible cluster.
				clusterEnd := end + len(cluster)
				return s[:clusterEnd], s[clusterEnd:]
			}
			return s[:end], s[end:]
		}
		used += clusterWidth
		end += len(cluster)
		if cluster == " " || cluster == "\t" {
			lastSpaceEnd = end
		}
	}
	return s, ""
}

func (p DisplayCellProfile) padAligned(
	s string,
	width int,
	align string,
	startColumn int,
) string {
	if width <= 0 {
		return ""
	}
	left := 0
	switch align {
	case "right":
		for candidate := 0; candidate <= width; candidate++ {
			contentWidth := p.measure(s, startColumn+candidate)
			if candidate+contentWidth <= width {
				left = candidate
			}
		}
	case "center":
		bestScore := width + 1
		for candidate := 0; candidate <= width; candidate++ {
			contentWidth := p.measure(s, startColumn+candidate)
			right := width - candidate - contentWidth
			if right < 0 {
				continue
			}
			score := candidate - right
			if score < 0 {
				score = -score
			}
			if score < bestScore {
				bestScore = score
				left = candidate
			}
		}
	}
	text := p.expandTabs(s, startColumn+left)
	contentWidth := p.measure(text, startColumn+left)
	right := maxInt(width-left-contentWidth, 0)
	return strings.Repeat(" ", left) + text + strings.Repeat(" ", right)
}

func (p DisplayCellProfile) clusterWidth(cluster string, measured int) int {
	if cluster == "" || isANSIControlCluster(cluster, measured) {
		return measured
	}

	// The deterministic project profile deliberately treats an unpaired
	// regional indicator as text. A paired flag remains the displaywidth
	// stack's two-cell grapheme.
	if runeCount := utf8.RuneCountInString(cluster); runeCount == 1 {
		r, _ := utf8.DecodeRuneInString(cluster)
		if r >= 0x1F1E6 && r <= 0x1F1FF {
			return p.policy.LoneRegionalIndicatorCell
		}
		// U+1F3F7 has text presentation in Unicode data but is rendered as a
		// two-cell label glyph by the terminals covered by the G9 contract.
		if r == 0x1F3F7 {
			return p.policy.BareLabelCells
		}
	} else if runeCount == 2 && regionalIndicatorCount(cluster) == 2 {
		return p.policy.PairedFlagCells
	}

	// Indic conjuncts joined by a script virama occupy the two-cell geometry
	// selected by the G9 terminal profile. Count letters instead of matching
	// fixture strings so the policy applies to the supported Indic scripts.
	letters := 0
	hasLinker := false
	for _, r := range cluster {
		if unicode.IsLetter(r) {
			letters++
		}
		if isIndicLinker(r) {
			hasLinker = true
		}
	}
	if hasLinker && letters >= 2 && measured < 2 {
		return p.policy.IndicConjunctCells
	}
	return measured
}

func regionalIndicatorCount(cluster string) int {
	count := 0
	for _, r := range cluster {
		if r >= 0x1F1E6 && r <= 0x1F1FF {
			count++
		}
	}
	return count
}

func isANSIControlCluster(cluster string, width int) bool {
	return width == 0 && len(cluster) > 0 && cluster[0] == '\x1b'
}

func isIndicLinker(r rune) bool {
	switch r {
	case 0x094D, // Devanagari
		0x09CD, // Bengali
		0x0A4D, // Gurmukhi
		0x0ACD, // Gujarati
		0x0B4D, // Oriya
		0x0BCD, // Tamil
		0x0C4D, // Telugu
		0x0CCD, // Kannada
		0x0D4D, // Malayalam
		0x0DCA: // Sinhala
		return true
	default:
		return false
	}
}

type displayCellControlState struct {
	sgrReplay string
	osc8Open  string
}

func (p DisplayCellProfile) balanceControlLines(lines []string) []string {
	state := displayCellControlState{}
	balanced := make([]string, len(lines))
	for index, line := range lines {
		prefix := state.replay()
		state.observe(p, line)
		balanced[index] = prefix + line + state.close()
	}
	return balanced
}

func (s *displayCellControlState) observe(
	profile DisplayCellProfile,
	line string,
) {
	iter := profile.options.StringGraphemes(line)
	for iter.Next() {
		sequence := iter.Value()
		if !isANSIControlCluster(sequence, iter.Width()) {
			continue
		}
		switch {
		case isSGRSequence(sequence):
			if isFullSGRReset(sequence) {
				s.sgrReplay = ""
			} else {
				// Replaying the ordered SGR delta preserves partial resets,
				// extended colors, and later overrides without constructing a
				// second style model.
				s.sgrReplay += sequence
			}
		case isOSC8Sequence(sequence):
			if isOSC8Close(sequence) {
				s.osc8Open = ""
			} else {
				s.osc8Open = sequence
			}
		}
	}
}

func (s displayCellControlState) replay() string {
	return s.osc8Open + s.sgrReplay
}

func (s displayCellControlState) close() string {
	var close strings.Builder
	if s.sgrReplay != "" {
		close.WriteString("\x1b[0m")
	}
	if s.osc8Open != "" {
		close.WriteString("\x1b]8;;\x1b\\")
	}
	return close.String()
}

func isSGRSequence(sequence string) bool {
	if len(sequence) < 3 ||
		!strings.HasPrefix(sequence, "\x1b[") ||
		!strings.HasSuffix(sequence, "m") {
		return false
	}
	parameters := sequence[2 : len(sequence)-1]
	for index := 0; index < len(parameters); index++ {
		value := parameters[index]
		if (value < '0' || value > '9') && value != ';' && value != ':' {
			return false
		}
	}
	return true
}

func isFullSGRReset(sequence string) bool {
	return sequence == "\x1b[m" || sequence == "\x1b[0m"
}

func isOSC8Sequence(sequence string) bool {
	if !strings.HasPrefix(sequence, "\x1b]8;") ||
		(!strings.HasSuffix(sequence, "\a") &&
			!strings.HasSuffix(sequence, "\x1b\\")) {
		return false
	}
	body := strings.TrimPrefix(sequence, "\x1b]8;")
	body = strings.TrimSuffix(body, "\a")
	body = strings.TrimSuffix(body, "\x1b\\")
	if !strings.Contains(body, ";") {
		return false
	}
	for _, r := range body {
		if unicode.IsControl(r) || isUnsafeDisplayCellRune(r) {
			return false
		}
	}
	return true
}

func isOSC8Close(sequence string) bool {
	if !isOSC8Sequence(sequence) {
		return false
	}
	body := strings.TrimPrefix(sequence, "\x1b]8;")
	body = strings.TrimSuffix(body, "\a")
	body = strings.TrimSuffix(body, "\x1b\\")
	parts := strings.SplitN(body, ";", 2)
	return len(parts) == 2 && parts[1] == ""
}
