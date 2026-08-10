# Render Correctness Test

This document exercises every markdown element that glamour supports.
Use it to verify visual rendering and measure performance in the TUI.

## Headings

All six heading levels should render with distinct styling:

# Heading Level 1

## Heading Level 2

### Heading Level 3

#### Heading Level 4

##### Heading Level 5

###### Heading Level 6

## Paragraphs and Inline Formatting

This is a normal paragraph with **bold text**, *italic text*, and ***bold italic*** combined. You can also use `inline code` within paragraphs. Here is ~~strikethrough text~~ for deleted content.

This is a second paragraph to verify paragraph separation works correctly. Long paragraphs should word-wrap properly without breaking words in the middle. The quick brown fox jumps over the lazy dog. Pack my box with five dozen liquor jugs. How vexingly quick daft zebras jump.

## Horizontal Rules

Below are three horizontal rules using different syntax:

---

Text between rules.

***

More text between rules.

___

Text after the last rule.

## Unordered Lists

- First item in a list
- Second item with more text that might wrap on narrow terminals to test word wrapping behavior
- Third item
  - Nested item one
  - Nested item two
    - Deeply nested item
- Back to top level

## Ordered Lists

1. First ordered item
2. Second ordered item
3. Third ordered item with longer text to verify wrapping
4. Fourth item
   1. Nested ordered item
   2. Another nested item
5. Back to top level

## Task Lists

- [x] Completed task
- [x] Another completed task
- [ ] Incomplete task
- [ ] Another incomplete task
  - [x] Nested completed
  - [ ] Nested incomplete

## Block Quotes

> This is a block quote. It should have a distinctive visual indicator
> like a vertical bar on the left side.

> Multi-paragraph block quote.
>
> This is the second paragraph within the same quote.

> Nested quotes:
> > This is a nested quote inside another quote.
> > It should show multiple levels of indentation.

## Code Blocks

Inline code: `fmt.Println("hello world")`

Fenced code block with syntax highlighting:

```go
package main

import (
    "fmt"
    "strings"
)

// StreamingMarkdown renders markdown content incrementally.
type StreamingMarkdown struct {
    width        int
    stablePrefix string
    stableRender string
}

func (s *StreamingMarkdown) Render(content string, width int) string {
    if content == "" {
        return ""
    }
    // Fast path: exact match with full cache
    if s.fullCacheSrc == content && s.width == width {
        return s.fullCache
    }
    return s.renderFull(content, width)
}

func main() {
    sm := &StreamingMarkdown{}
    result := sm.Render("# Hello World\n\nThis is a test.", 80)
    fmt.Println(result)
    
    // Multi-line string
    lines := strings.Split(result, "\n")
    for i, line := range lines {
        fmt.Printf("%3d: %s\n", i+1, line)
    }
}
```

Another code block in Python:

```python
def fibonacci(n: int) -> list[int]:
    """Generate fibonacci sequence up to n terms."""
    if n <= 0:
        return []
    elif n == 1:
        return [0]
    
    fib = [0, 1]
    for i in range(2, n):
        fib.append(fib[i-1] + fib[i-2])
    return fib

# Usage
result = fibonacci(10)
print(f"First 10 fibonacci numbers: {result}")
```

Plain code block (no language specified):

```
This is a plain code block without syntax highlighting.
It should still render with a distinct background or styling.
Multiple lines are preserved as-is.
    Indentation is preserved too.
```

## Tables

| Feature | Supported | Notes |
|---------|-----------|-------|
| H1 Heading | Yes | Colored background |
| H2-H6 Headings | Yes | Bold + colored |
| Horizontal Rules | Yes | Heavy line character |
| Code Blocks | Yes | Syntax highlighting |
| Tables | Yes | Aligned columns |
| Lists | Yes | Bullet + numbered |
| Block Quotes | Yes | Vertical bar indent |
| LaTeX/Math | No | Not in glamour v1 |
| Footnotes | No | Not in glamour v1 |

Wider table with more columns:

| Method | Time Complexity | Space Complexity | Stable | In-Place | Best For |
|--------|----------------|-----------------|--------|----------|----------|
| Bubble Sort | O(n²) | O(1) | Yes | Yes | Small datasets |
| Quick Sort | O(n log n) | O(log n) | No | Yes | General purpose |
| Merge Sort | O(n log n) | O(n) | Yes | No | Linked lists |
| Heap Sort | O(n log n) | O(1) | No | Yes | Memory constrained |
| Radix Sort | O(nk) | O(n+k) | Yes | No | Integer keys |

## Links

Here is a [link to Go documentation](https://go.dev/doc/) inline.

Another [link with title](https://github.com "GitHub Homepage") that has hover text.

## Images

![Go Gopher](https://go.dev/blog/gopher/header.jpg)

## Combined Elements

This section tests multiple elements combined in realistic output:

### Implementation Plan

The rendering pipeline has three main stages:

1. **Content Detection**: Check if content contains markdown syntax
   - Uses regex: `[#*` + `` ` `` + `|\\[>\\-_~]`
   - Fast path returns plain text if no syntax detected

2. **Boundary Detection**: Find safe split points for prefix caching
   - Walks backward through blank-line positions
   - Validates no open constructs (fences, lists, tables)
   - Returns `-1` if no safe boundary exists

3. **Render + Cache**: Render prefix and trailing separately
   - Prefix render is cached across streaming flushes
   - Trailing partial is re-rendered each frame
   - Results glued with `"\n\n"` separator

> **Note**: The boundary detection is deliberately conservative.
> When in doubt, it falls back to a full render rather than risk
> corrupting the output by splitting inside a markdown construct.

### Performance Characteristics

| Operation | Cost | When |
|-----------|------|------|
| First render | O(content) | Width change or new message |
| Streaming flush | O(trailing) | Delta appended |
| Cached hit | O(1) | Same content + width |
| Cache miss | O(content) | No safe boundary found |

Here is some code showing the critical path:

```go
boundary := findSafeMarkdownBoundary(content)
if boundary < 0 {
    // No safe boundary — full render, cache untouched
    return full()
}
if boundary <= len(s.stablePrefix) {
    // Cached prefix covers boundary — render trailing only
    trail := content[len(s.stablePrefix):]
    return glueRenders(s.stableRender, renderTrailing(trail))
}
// New safe content — promote boundary and render
```

---

## Stress Test: Long Content

The following section is intentionally long to test rendering performance with scroll.

### Section Alpha

Lorem ipsum dolor sit amet, consectetur adipiscing elit. Sed do eiusmod tempor incididunt ut labore et dolore magna aliqua. Ut enim ad minim veniam, quis nostrud exercitation ullamco laboris nisi ut aliquip ex ea commodo consequat. Duis aute irure dolor in reprehenderit in voluptate velit esse cillum dolore eu fugiat nulla pariatur. Excepteur sint occaecat cupidatat non proident, sunt in culpa qui officia deserunt mollit anim id est laborum.

### Section Beta

Curabitur pretium tincidunt lacus. Nulla gravida orci a odio. Nullam varius, turpis et commodo pharetra, est eros bibendum elit, nec luctus magna felis sollicitudin mauris. Integer in mauris eu nibh euismod gravida. Duis ac tellus et risus vulputate vehicula. Donec lobortis risus a elit.

- Point one about performance optimization
- Point two about cache invalidation strategies
- Point three about memory allocation patterns
- Point four about concurrent access safety

### Section Gamma

Here we test deeply nested structures:

1. Top-level ordered item
   - Unordered sub-item
   - Another sub-item with `code` in it
     1. Deep ordered item
     2. Another deep item
        - Even deeper
        - And deeper still
   - Back to unordered
2. Second top-level item

> A quote within the stress test section.
> 
> > Nested quote for good measure.
> > With multiple lines to test wrapping behavior
> > when the terminal is narrow.
>
> Back to single-level quote.

### Section Delta

Final section with a code block to end on:

```bash
#!/bin/bash
# Build and test the TUI rendering
cd /path/to/eino-agent
go build ./cmd/eino-agent/
echo "Build successful"

# Run with test content
echo "# Test Heading

Paragraph text.

## Second Heading

- List item 1
- List item 2

---

Done." | ./eino-agent --test-render
```

## Summary

This file tests the following glamour-supported elements:

| Element | Count in Document |
|---------|-------------------|
| H1 | 2 |
| H2 | 9 |
| H3 | 6 |
| H4 | 1 |
| H5 | 1 |
| H6 | 1 |
| Paragraphs | 15+ |
| Horizontal Rules | 4 |
| Unordered Lists | 4 |
| Ordered Lists | 3 |
| Task Lists | 1 |
| Block Quotes | 5 |
| Code Blocks | 5 |
| Tables | 4 |
| Links | 2 |
| Images | 1 |
| Inline Code | 8+ |
| Bold | 5+ |
| Italic | 2+ |
| Strikethrough | 1 |

**Not supported by glamour v1.0.0:**
- LaTeX/Math (`$...$`, `$$...$$`)
- Footnotes (`[^1]`)
- These render as plain text, not as formatted elements.
