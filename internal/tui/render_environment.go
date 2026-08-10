package tui

// RenderEnvironment is the immutable App-owned input to Markdown geometry.
// Theme and geometry generations are intentionally independent.
type RenderEnvironment struct {
	styles       Styles
	themeGen     uint64
	geometryGen  uint64
	profile      DisplayCellProfile
	rendererPool *markdownRendererPool
}

type renderEnvironmentIdentity struct {
	theme                ThemeName
	themeGen             uint64
	geometryGen          uint64
	displayCellProfileID string
}

func newRenderEnvironment(styles Styles, profile DisplayCellProfile) RenderEnvironment {
	if !profile.valid() {
		profile = DefaultDisplayCellProfile()
	}
	return RenderEnvironment{
		styles:       styles,
		themeGen:     1,
		geometryGen:  1,
		profile:      profile,
		rendererPool: newMarkdownRendererPool(defaultMarkdownRendererPoolCapacity),
	}
}

func defaultRenderEnvironment(styles Styles) RenderEnvironment {
	return newRenderEnvironment(styles, DefaultDisplayCellProfile())
}

func (e RenderEnvironment) normalized() RenderEnvironment {
	if !e.profile.valid() {
		e.profile = DefaultDisplayCellProfile()
	}
	if e.themeGen == 0 {
		e.themeGen = 1
	}
	if e.geometryGen == 0 {
		e.geometryGen = 1
	}
	if e.rendererPool == nil {
		e.rendererPool = newMarkdownRendererPool(defaultMarkdownRendererPoolCapacity)
	}
	return e
}

func (e RenderEnvironment) withRendererPool() RenderEnvironment {
	if e.rendererPool == nil {
		e.rendererPool = newMarkdownRendererPool(defaultMarkdownRendererPoolCapacity)
	}
	return e
}

func (e RenderEnvironment) identity() renderEnvironmentIdentity {
	// Package tests retain a synthetic non-empty profile ID seam for proving
	// cache separation without constructing a second supported policy.
	if e.profile.id == "" {
		e.profile = DefaultDisplayCellProfile()
	}
	if e.themeGen == 0 {
		e.themeGen = 1
	}
	if e.geometryGen == 0 {
		e.geometryGen = 1
	}
	return renderEnvironmentIdentity{
		theme:                e.styles.theme,
		themeGen:             e.themeGen,
		geometryGen:          e.geometryGen,
		displayCellProfileID: e.profile.Identity(),
	}
}

func (e RenderEnvironment) withStyles(styles Styles) RenderEnvironment {
	e = e.normalized()
	e.styles = styles
	e.themeGen++
	return e
}

func (e RenderEnvironment) withGeometry() RenderEnvironment {
	e = e.normalized()
	e.geometryGen++
	return e
}
