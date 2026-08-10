package tui

import (
	"fmt"
	"slices"
	"sort"
	"strings"
	"testing"

	agenttools "github.com/abietic/yhc/tools"
)

var auditedGenericToolNames = []string{
	"AskUserQuestion",
	"Brief",
	"Config",
	"LSP",
	"ListMcpResourcesTool",
	"McpAuth",
	"Monitor",
	"NotebookEdit",
	"ReadMcpResourceTool",
	"ScheduleCron",
	"ScheduleWakeup",
	"SendMessage",
	"Skill",
	"Sleep",
	"TeamCreate",
	"TeamDelete",
	"ToolSearch",
	"get_goal",
	"update_goal",
}

func TestToolHistoryRendererInventoryClassifiesEveryDefaultTool(t *testing.T) {
	previousRegistry := agenttools.DefaultRegistry
	registry := agenttools.NewRegistry()
	agenttools.RegisterDefaults(registry)
	t.Cleanup(func() {
		agenttools.DefaultRegistry = previousRegistry
	})

	infos := registry.List()
	if len(infos) != 41 {
		t.Fatalf("default tool inventory = %d, want 41", len(infos))
	}
	genericNames := make([]string, 0, len(auditedGenericToolNames))
	for _, info := range infos {
		renderer := toolHistoryRendererFor(info.Name)
		switch renderer.(type) {
		case genericToolHistoryRenderer:
			genericNames = append(genericNames, info.Name)
		case bashToolHistoryRenderer,
			readSearchToolHistoryRenderer,
			editWriteToolHistoryRenderer,
			agentToolHistoryRenderer,
			mcpToolHistoryRenderer,
			planTaskTodoToolHistoryRenderer,
			webToolHistoryRenderer:
		default:
			t.Fatalf("tool %s has unaudited renderer %T", info.Name, renderer)
		}
	}
	sort.Strings(genericNames)
	expected := append([]string(nil), auditedGenericToolNames...)
	sort.Strings(expected)
	if !slices.Equal(genericNames, expected) {
		t.Fatalf("generic inventory = %v, want %v", genericNames, expected)
	}
}

func TestGenericHistoryRendererBoundedAndFullProjections(t *testing.T) {
	lines := []string{"first " + strings.Repeat("界", 200), "line-02", "line-03", "line-04"}
	for index := 5; index <= 12; index++ {
		lines = append(lines, fmt.Sprintf("line-%02d", index))
	}
	output := "\x1b[31m" + strings.Join(lines, "\n") + "\x1b[0m"
	tool := &ToolMessage{
		toolCallID: "plugin-call-1",
		name:       "PluginCustomTool",
		input:      `{"credentials":"super-secret","message":"inspect data","token":"token-secret"}`,
		output:     output,
		status:     ToolSuccess,
		version:    1,
	}
	rendered := tool.Render(120, defaultStyles())
	plain := stripANSIForTest(rendered)
	for _, want := range []string{"PluginCustomTool", "completed", "[redacted]", "line-02", "line-04", "+5 lines", "line-12", "content clipped"} {
		if !strings.Contains(plain, want) {
			t.Fatalf("generic rich missing %q: %q", want, plain)
		}
	}
	if strings.Contains(plain, "super-secret") || strings.Contains(plain, "token-secret") || strings.Contains(plain, "line-06") {
		t.Fatalf("generic rich leaked secret or unbounded output: %q", plain)
	}
	if lineCount := len(strings.Split(rendered, "\n")); lineCount > 10 {
		t.Fatalf("generic rich lines = %d, want <= 10", lineCount)
	}
	assertHistoryLinesFit(t, rendered, 120)

	expanded := stripANSIForTest(tool.RenderExpanded(HistoryRenderContext{Width: 64, Styles: defaultStyles()}))
	for _, want := range []string{"Input:", "super-secret", "Result:", "line-06", "line-12"} {
		if !strings.Contains(expanded, want) {
			t.Fatalf("generic expanded missing %q: %q", want, expanded)
		}
	}
	assertHistoryLinesFit(t, tool.RenderExpanded(HistoryRenderContext{Width: 64, Styles: defaultStyles()}), 64)

	raw := tool.RenderRaw(HistoryRenderContext{Width: 12, Styles: defaultStyles()})
	for _, want := range []string{"PluginCustomTool", "Status: completed", "super-secret", "line-06", "line-12"} {
		if !strings.Contains(raw, want) {
			t.Fatalf("generic raw missing %q: %q", want, raw)
		}
	}
	if strings.Contains(raw, "\x1b[") {
		t.Fatalf("generic raw contains terminal controls: %q", raw)
	}
}

func TestGenericHistoryRendererLifecycleMalformedUnicodeAndEmpty(t *testing.T) {
	running := &ToolMessage{
		name: "插件工具", input: `{未完成`, status: ToolRunning, version: 2, spinnerCount: 1,
	}
	runningRendered := running.Render(24, defaultStyles())
	if plain := stripANSIForTest(runningRendered); !strings.Contains(plain, "插件工具") || !strings.Contains(plain, "running") {
		t.Fatalf("generic running = %q", plain)
	}
	assertHistoryLinesFit(t, runningRendered, 24)

	failed := &ToolMessage{
		name: "PluginFailure", input: `{bad`,
		output: strings.Repeat("remote failure detail ", 20), status: ToolError, version: 1,
	}
	failedRendered := failed.Render(30, defaultStyles())
	failedPlain := stripANSIForTest(failedRendered)
	if !strings.Contains(failedPlain, "failed") || !strings.Contains(failedPlain, "remote failure") || !strings.Contains(failedPlain, "content clipped") {
		t.Fatalf("generic failure = %q", failedPlain)
	}
	assertHistoryLinesFit(t, failedRendered, 30)

	empty := &ToolMessage{name: "PluginEmpty", input: `{}`, status: ToolSuccess, version: 1}
	if plain := stripANSIForTest(empty.Render(48, defaultStyles())); !strings.Contains(plain, "completed") || !strings.Contains(plain, "(no content)") {
		t.Fatalf("generic empty = %q", plain)
	}
}

func TestGenericHistoryRendererRedactsAuthAndConfigHeaders(t *testing.T) {
	auth := &ToolMessage{
		name:   "McpAuth",
		input:  `{"auth_type":"api_key","credentials":"credential-secret","server_name":"docs"}`,
		output: "authenticated", status: ToolSuccess, version: 1,
	}
	authPlain := stripANSIForTest(auth.Render(140, defaultStyles()))
	if !strings.Contains(authPlain, "credentials: [redacted]") || strings.Contains(authPlain, "credential-secret") {
		t.Fatalf("McpAuth header redaction = %q", authPlain)
	}

	config := &ToolMessage{
		name:   "Config",
		input:  `{"action":"set","key":"OPENAI_API_KEY","value":"config-secret"}`,
		output: "updated", status: ToolSuccess, version: 1,
	}
	configPlain := stripANSIForTest(config.Render(140, defaultStyles()))
	if !strings.Contains(configPlain, "value: [redacted]") || strings.Contains(configPlain, "config-secret") {
		t.Fatalf("Config header redaction = %q", configPlain)
	}
}

func TestGenericHistoryRendererUsesSemanticCacheIdentity(t *testing.T) {
	chat := NewChatView(defaultStyles())
	chat.SetSize(80, 8)
	chat.AppendToolStart("plugin-call-cache", "PluginCachedTool", `{"value":"input"}`)
	chat.UpdateToolResult("plugin-call-cache", "PluginCachedTool", "complete output")
	chat.Render(80, 8)

	if len(chat.items) != 1 {
		t.Fatalf("cached generic items = %d", len(chat.items))
	}
	key := chat.historyCacheKeys[chat.items[0]]
	if key != "tool:plugin-call-cache" {
		t.Fatalf("cached generic semantic key = %q", key)
	}
	entry := chat.renderCache[key]
	if entry == nil || !entry.frozen {
		t.Fatalf("cached generic entry = %#v", entry)
	}
	history := chat.HistoryItems()
	if len(history) != 1 || !strings.Contains(history[0].Raw(HistoryRenderContext{Width: 8}), "complete output") {
		t.Fatalf("cached generic raw history = %#v", history)
	}
}
