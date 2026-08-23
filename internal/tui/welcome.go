package tui

import (
	"fmt"
	"math"
	"math/rand"
	"os"
	"path/filepath"
	"runtime/debug"
	"strings"
	"time"

	tea "charm.land/bubbletea/v2"
	"charm.land/lipgloss/v2"
	xansi "github.com/charmbracelet/x/ansi"

	"github.com/abietic/yhc/engine/permission"
	"github.com/abietic/yhc/engine/services"
	"github.com/abietic/yhc/internal/identity"
	"github.com/abietic/yhc/internal/tui/terminalcap"
)

var welcomeGreetings = []string{
	"Welcome back. What are we building?",
	"Ready when you are.",
	"Let's turn an idea into working code.",
}

var welcomeTips = []string{
	"Use /commands for available slash commands",
	"Press ? for keybinding help",
	"Use ! to run a shell command",
	"Use /mode to change permission mode",
}

func (a *App) rotateWelcomeTip() {
	if a == nil || a.engine == nil || a.welcomeTipPinned || !a.engine.LongSessionServicesEnabled() {
		return
	}
	path := filepath.Join(a.engine.GetTranscriptDir(), "tip-history.json")
	if path == a.welcomeTipHistoryPath {
		return
	}
	history, err := services.NewPersistentTipHistory(path)
	if err != nil {
		return
	}
	tip := services.NewTipScheduler(services.NewTipRegistry(), history).NextTip()
	if tip == nil {
		return
	}
	history.MarkShown(tip.ID)
	a.welcomeTip = tip.Content
	a.welcomeTipHistoryPath = path
}

func getVersion() string {
	if Version != "" && Version != "dev" {
		return Version
	}
	if bi, ok := debug.ReadBuildInfo(); ok && bi.Main.Version != "" && bi.Main.Version != "(devel)" {
		return bi.Main.Version
	}
	return "dev"
}

type welcomeTier int

const (
	welcomeTooSmall welcomeTier = iota
	welcomeCompactText
	welcomeCondensedMascot
	welcomeFullBordered
)

func welcomeTierFor(width, height int) welcomeTier {
	if width < 40 || height < 12 {
		return welcomeTooSmall
	}
	if width <= 56 {
		return welcomeCompactText
	}
	// The 6-row Eno plus border and tip needs more height than the condensed
	// layout; below it the full box would clip the chin row.
	if width <= 69 || height < 15 {
		return welcomeCondensedMascot
	}
	return welcomeFullBordered
}

func (a *App) mascotVisible() bool {
	return a.state == StateWelcome && welcomeTierFor(a.width, a.height) >= welcomeCondensedMascot
}

func (a *App) mascotFrameLines() [MascotHeight]string {
	frame := Frame{Pose: PoseDefault}
	if a.mascotAnim != nil {
		frame = a.mascotAnim.CurrentFrame()
	}
	art := renderMascotStyled(frame.Pose, a.styles.enoTones())
	if frame.Offset == 0 {
		return art
	}
	return [MascotHeight]string{"", art[0], art[1], art[2], art[3], art[4]}
}

type mascotIdleTickMsg struct {
	generation uint64
}

type mascotIdleAfterFunc func(time.Duration, uint64) tea.Cmd

func defaultMascotIdleAfter(delay time.Duration, generation uint64) tea.Cmd {
	return tea.Tick(delay, func(time.Time) tea.Msg {
		return mascotIdleTickMsg{generation: generation}
	})
}

func (a *App) mascotIdleUnit() float64 {
	value := rand.Float64()
	if a.mascotIdleRand != nil {
		value = a.mascotIdleRand()
	}
	switch {
	case math.IsNaN(value), value < 0:
		return 0
	case value > 1:
		return 1
	default:
		return value
	}
}

func (a *App) mascotIdleDelay() time.Duration {
	return 3*time.Second + time.Duration(a.mascotIdleUnit()*float64(2*time.Second))
}

func (a *App) mascotIdleSequence() []Frame {
	if a.mascotIdleUnit() >= 0.25 {
		return hold(PoseBlink, 0, 3)
	}
	pose := PoseLookLeft
	if a.mascotIdleUnit() < 0.5 {
		pose = PoseLookRight
	}
	frameCount := 7 + int(a.mascotIdleUnit()*3)
	if frameCount > 9 {
		frameCount = 9
	}
	return hold(pose, 0, frameCount)
}

func (a *App) mascotIdleContextEligible() bool {
	return a != nil &&
		!a.reducedMotion &&
		a.mascotVisible() &&
		a.mascotAnim != nil
}

func (a *App) mascotIdleEligible() bool {
	return a.mascotIdleContextEligible() && !a.mascotAnim.Active()
}

// ensureMascotIdleTick owns the single 3–5 second delay chain. Each schedule
// receives a new generation because tea.Tick itself cannot be cancelled.
func (a *App) ensureMascotIdleTick() tea.Cmd {
	if !a.mascotIdleEligible() || a.mascotIdleScheduled {
		return nil
	}
	a.mascotIdleGeneration++
	generation := a.mascotIdleGeneration
	a.mascotIdleScheduled = true
	after := a.mascotIdleAfter
	if after == nil {
		after = defaultMascotIdleAfter
	}
	return after(a.mascotIdleDelay(), generation)
}

func (a *App) invalidateMascotIdle() {
	if a == nil || !a.mascotIdleScheduled {
		return
	}
	a.mascotIdleScheduled = false
	a.mascotIdleGeneration++
}

func (a *App) stopMascotIdle() {
	if a == nil {
		return
	}
	a.invalidateMascotIdle()
	if a.mascotAnim != nil {
		a.mascotAnim.Stop()
	}
}

func (a *App) reconcileMascotIdle() tea.Cmd {
	if !a.mascotIdleContextEligible() {
		a.stopMascotIdle()
		return nil
	}
	return a.ensureMascotIdleTick()
}

func (a *App) acceptMascotIdleTick(generation uint64) bool {
	if a == nil || !a.mascotIdleScheduled || generation != a.mascotIdleGeneration {
		return false
	}
	a.mascotIdleScheduled = false
	return a.mascotIdleEligible()
}

func (a *App) welcomeMode() string {
	if a.inputMode == InputShell {
		return "shell input"
	}
	if a.inputMode == InputCommand {
		return "command input"
	}
	switch a.permissionMode() {
	case permission.ModePlan:
		return "plan mode"
	case permission.ModeBypassPermissions:
		return "bypass permissions"
	case permission.ModeAuto:
		return "auto permissions"
	default:
		return "default permissions"
	}
}

func (a *App) welcomeModel() string {
	if a.model == "" {
		return "model not configured"
	}
	return a.model
}

func (a *App) renderWelcome() string {
	return a.renderWelcomeBudget(100)
}

func (a *App) renderWelcomeBudget(maxLines int) string {
	if maxLines <= 0 {
		return ""
	}
	width := a.width
	if width == 0 {
		width = 80
	}
	profile := a.renderEnvironment.normalized().profile

	title := renderWelcomeWordmark(
		a.styles,
		a.terminalCaps.Color == terminalcap.ColorTrueColor,
	) + a.styles.Dim.Render(" v"+getVersion())
	details := a.styles.Dim.Render(truncateDisplayWithProfile(
		profile,
		a.welcomeModel()+" · "+a.welcomeMode(),
		max(10, width-4),
		0,
	))
	dimensions := a.styles.Dim.Render(fmt.Sprintf("%dx%d", a.width, a.height))
	dimWidth := profile.measure(dimensions, 0)
	cwd := a.styles.Dim.Render(truncatePathDisplayWithProfile(
		profile,
		a.cwdShort(),
		max(10, width-4-dimWidth-3),
		0,
	))
	tip := a.styles.Subtle.Render("Tip: " + a.welcomeTip)

	var lines []string
	switch welcomeTierFor(width, a.height) {
	case welcomeCompactText:
		lines = []string{
			centerLineWithProfile(profile, title, width),
			centerLineWithProfile(
				profile,
				truncateDisplayWithProfile(profile, a.welcomeGreeting, width-4, 0),
				width,
			),
			centerLineWithProfile(profile, details, width),
			centerLineWithProfile(profile, cwd+" · "+dimensions, width),
			centerLineWithProfile(
				profile,
				truncateDisplayWithProfile(profile, tip, width-2, 0),
				width,
			),
		}
	case welcomeCondensedMascot:
		art := a.mascotFrameLines()
		textWidth := width - MascotWidth - 3
		text := []string{
			title,
			a.styles.Dim.Render(truncateDisplayWithProfile(
				profile,
				a.welcomeModel()+" · "+a.welcomeMode(),
				textWidth,
				MascotWidth+2,
			)),
			a.styles.Dim.Render(truncatePathDisplayWithProfile(
				profile,
				a.cwdShort(),
				max(1, textWidth-dimWidth-1),
				MascotWidth+2,
			)) + " " + dimensions,
		}
		blockWidth := MascotWidth + 2 + maxLineWidthWithProfile(profile, text, MascotWidth+2)
		left := max(0, (width-blockWidth)/2)
		textOffset := max(0, (MascotHeight-len(text))/2)
		for i := range art {
			line := ""
			if textIndex := i - textOffset; textIndex >= 0 && textIndex < len(text) {
				line = text[textIndex]
			}
			lines = append(lines, strings.Repeat(" ", left)+art[i]+"  "+line)
		}
		lines = append(lines, centerLineWithProfile(
			profile,
			truncateDisplayWithProfile(profile, tip, width-2, 0),
			width,
		))
	case welcomeFullBordered:
		art := a.mascotFrameLines()
		boxWidth := min(width-4, 76)
		innerWidth := boxWidth - 2
		textWidth := innerWidth - MascotWidth - 3
		text := []string{
			title + "  " + a.styles.Bold.Render(truncateDisplayWithProfile(
				profile,
				a.welcomeGreeting,
				max(8, textWidth-profile.measure(title, 0)-2),
				MascotWidth+3+profile.measure(title, 0)+2,
			)),
			a.styles.Dim.Render(truncateDisplayWithProfile(
				profile,
				a.welcomeModel()+" · "+a.welcomeMode(),
				textWidth,
				MascotWidth+3,
			)),
			a.styles.Dim.Render(truncatePathDisplayWithProfile(
				profile,
				a.cwdShort(),
				max(1, textWidth-dimWidth-2),
				MascotWidth+3,
			)) + "  " + dimensions,
		}
		content := make([]string, 0, MascotHeight)
		textOffset := max(0, (MascotHeight-len(text))/2)
		for i := range art {
			line := ""
			if textIndex := i - textOffset; textIndex >= 0 && textIndex < len(text) {
				line = truncateDisplayWithProfile(
					profile,
					text[textIndex],
					textWidth,
					MascotWidth+3,
				)
			}
			content = append(content, art[i]+"  "+line)
		}
		boxStyle := lipgloss.NewStyle().
			Border(lipgloss.RoundedBorder()).
			BorderForeground(a.styles.EnoBody.GetForeground()).
			Padding(0, 1)
		box := contentRenderStyleWidth(
			profile,
			boxStyle,
			innerWidth,
			strings.Join(content, "\n"),
		)
		for _, line := range strings.Split(box, "\n") {
			lines = append(lines, centerLineWithProfile(profile, line, width))
		}
		lines = append(lines, centerLineWithProfile(
			profile,
			truncateDisplayWithProfile(profile, tip, width-2, 0),
			width,
		))
	default:
		return ""
	}

	if len(lines) > maxLines {
		lines = lines[:maxLines]
	}
	return strings.Join(contentProjectRows(profile, lines, width, 0), "\n")
}

// renderWelcomeWordmark renders the static Revontuli identity from the
// App-owned style snapshot. Truecolor terminals receive the horizontal
// brand → sky → permission gradient; reduced-color terminals retain the
// established flat Header style and its padding.
func renderWelcomeWordmark(styles Styles, truecolor bool) string {
	const text = identity.ProductName
	if !truecolor {
		return styles.Header.Render(text)
	}
	brandRGB, brandOK := truecolorRGB(styleForegroundString(styles.Header))
	skyRGB, skyOK := truecolorRGB(styleForegroundString(styles.AuroraSky))
	violetRGB, violetOK := truecolorRGB(styleForegroundString(styles.DialogTitle))
	if !brandOK || !skyOK || !violetOK {
		return styles.Header.Render(text)
	}

	runes := []rune(text)
	if len(runes) == 0 {
		return ""
	}
	var wordmark strings.Builder
	for index, character := range runes {
		phase := float64(index) / float64(max(1, len(runes)-1))
		var color [3]uint8
		if phase < 0.5 {
			color = interpolateRGB(brandRGB, skyRGB, phase*2)
		} else {
			color = interpolateRGB(skyRGB, violetRGB, (phase-0.5)*2)
		}
		style := styles.Header.Foreground(lipgloss.Color(
			fmt.Sprintf("#%02X%02X%02X", color[0], color[1], color[2]),
		))
		if index > 0 {
			style = style.PaddingLeft(0)
		}
		wordmark.WriteString(style.Render(string(character)))
	}
	return wordmark.String()
}

func (a *App) cwdShort() string {
	dir := a.cwd()
	if home, err := os.UserHomeDir(); err == nil && home != "" && (dir == home || strings.HasPrefix(dir, home+string(filepath.Separator))) {
		return "~" + strings.TrimPrefix(dir, home)
	}
	if dir == "" {
		return "."
	}
	return dir
}

func truncateDisplayWithProfile(
	profile DisplayCellProfile,
	value string,
	width, startColumn int,
) string {
	return contentEllipsize(profile, value, width, startColumn, "…")
}

func truncatePathDisplayWithProfile(
	profile DisplayCellProfile,
	path string,
	width, startColumn int,
) string {
	return modalTruncatePath(profile, path, width, startColumn)
}

func centerLineWithProfile(
	profile DisplayCellProfile,
	line string,
	width int,
) string {
	startColumn := modalCenteredStartColumn(profile, []string{line}, width)
	return strings.Repeat(" ", startColumn) +
		contentProjectLine(profile, line, width-startColumn, startColumn)
}

func maxLineWidthWithProfile(
	profile DisplayCellProfile,
	lines []string,
	startColumn int,
) int {
	w := 0
	for _, line := range lines {
		w = max(w, profile.measure(line, startColumn))
	}
	return w
}

func lineCount(s string) int {
	if s == "" {
		return 0
	}
	return strings.Count(s, "\n") + 1
}

func firstLines(s string, n int) string {
	if n <= 0 || s == "" {
		return ""
	}
	lines := strings.Split(s, "\n")
	if len(lines) > n {
		lines = lines[:n]
	}
	return strings.Join(lines, "\n")
}

func (a *App) renderWelcomeView() string {
	editor := a.renderEditor()
	status := a.renderStatus()
	hints := a.renderHintSection()

	// Keep the editor and status visible even when hints expand in a short terminal.
	maxHintLines := max(0, a.height-lineCount(editor)-lineCount(status)-3)
	hints = firstLines(hints, maxHintLines)
	fixed := lineCount(editor) + lineCount(status) + lineCount(hints)
	sectionCount := 2
	if hints != "" {
		sectionCount++
	}
	welcomeBudget := max(0, a.height-fixed-sectionCount)
	welcome := a.renderWelcomeBudget(welcomeBudget)

	sections := make([]string, 0, 4)
	if welcome != "" {
		sections = append(sections, welcome)
	}
	if hints != "" {
		sections = append(sections, hints)
	}
	sections = append(sections, editor, status)
	return strings.Join(sections, "\n")
}

type mascotBounds struct{ x, y, width, height int }

func (a *App) welcomeMascotBounds() (mascotBounds, bool) {
	if !a.mascotVisible() {
		return mascotBounds{}, false
	}
	offset := 0
	if a.mascotAnim != nil {
		offset = a.mascotAnim.CurrentOffset()
	}
	lines := strings.Split(xansi.Strip(a.renderWelcome()), "\n")
	for y, line := range lines {
		// The head row starts with " ▗" in every pose. Its leading space is
		// the art's left edge, two rows below the top of the fixed viewport.
		if x := strings.Index(line, " ▗"); x >= 0 {
			return mascotBounds{
				x:      a.renderEnvironment.normalized().profile.measure(line[:x], 0),
				y:      y - 2 - offset,
				width:  MascotWidth,
				height: MascotHeight,
			}, true
		}
	}
	return mascotBounds{}, false
}

func (a *App) handleMascotMouse(msg tuiMouseMsg) (tea.Cmd, bool) {
	if msg.Button != tea.MouseLeft || msg.Action != mouseActionPress || !a.fullscreen || !a.mouseEnabled || a.reducedMotion {
		return nil, false
	}
	bounds, ok := a.welcomeMascotBounds()
	if !ok || msg.X < bounds.x || msg.X >= bounds.x+bounds.width || msg.Y < bounds.y || msg.Y >= bounds.y+bounds.height {
		return nil, false
	}
	a.invalidateMascotIdle()
	return a.mascotAnim.TriggerAnimation(), true
}
