package main

import (
	"fmt"
	"sort"
	"strings"
)

// renderFormattedMarkdown produces a markdown document with YAML frontmatter
// tailored to the given output format ("obsidian" or "notion").
// It combines metadata, transcripts, and notes into a single .md file
// ready for import into the target knowledge management tool.
func renderFormattedMarkdown(format string, meta *Metadata, transcriptText string) string {
	switch format {
	case "obsidian":
		return renderObsidian(meta, transcriptText)
	case "notion":
		return renderNotion(meta, transcriptText)
	default:
		return ""
	}
}

// ── Obsidian ─────────────────────────────────────────────────────────────────

func renderObsidian(meta *Metadata, transcriptText string) string {
	var b strings.Builder

	b.WriteString("---\n")
	writeYAMLField(&b, "title", meta.Title)
	if meta.Date != "" {
		writeYAMLField(&b, "date", dateFromISO(meta.Date))
	}
	writeYAMLField(&b, "grain_id", meta.ID)

	tags := flattenStringSlice(meta.Tags)
	tags = append([]string{"grain", "meeting"}, tags...)
	writeYAMLList(&b, "tags", tags)

	if participants := flattenStringSlice(meta.Participants); len(participants) > 0 {
		writeYAMLList(&b, "participants", participants)
	}

	if dur := formatDuration(meta.DurationSeconds); dur != "" {
		writeYAMLField(&b, "duration", dur)
	}

	if meta.Title != "" {
		writeYAMLList(&b, "aliases", []string{meta.Title})
	}

	if meta.Links.Grain != "" {
		writeYAMLField(&b, "grain_url", meta.Links.Grain)
	}
	if meta.Links.Share != "" {
		writeYAMLField(&b, "share_url", meta.Links.Share)
	}
	if meta.Links.Video != "" {
		writeYAMLField(&b, "video_url", meta.Links.Video)
	}

	b.WriteString("---\n\n")

	// Body
	b.WriteString("# ")
	b.WriteString(coalesce(meta.Title, meta.ID))
	b.WriteString("\n")

	if notes := formatAny(meta.AINotes); notes != "" {
		b.WriteString("\n## AI Notes\n\n")
		b.WriteString(notes)
		b.WriteString("\n")
	}

	if highlights := formatAny(meta.Highlights); highlights != "" {
		b.WriteString("\n## Highlights\n\n")
		b.WriteString(highlights)
		b.WriteString("\n")
	}

	if transcriptText != "" {
		b.WriteString("\n## Transcript\n\n")
		b.WriteString(transcriptText)
		b.WriteString("\n")
	}

	return b.String()
}

// ── Notion ───────────────────────────────────────────────────────────────────

func renderNotion(meta *Metadata, transcriptText string) string {
	var b strings.Builder

	b.WriteString("---\n")
	writeYAMLField(&b, "title", meta.Title)
	writeYAMLField(&b, "type", "Meeting")
	writeYAMLField(&b, "status", "Exported")
	if meta.Date != "" {
		writeYAMLField(&b, "date", dateFromISO(meta.Date))
	}
	writeYAMLField(&b, "grain_id", meta.ID)

	tags := flattenStringSlice(meta.Tags)
	tags = append([]string{"grain", "meeting"}, tags...)
	writeYAMLList(&b, "tags", tags)

	if participants := flattenStringSlice(meta.Participants); len(participants) > 0 {
		writeYAMLList(&b, "participants", participants)
	}

	if dur := formatDuration(meta.DurationSeconds); dur != "" {
		writeYAMLField(&b, "duration", dur)
	}

	if meta.Links.Grain != "" {
		writeYAMLField(&b, "grain_url", meta.Links.Grain)
	}
	if meta.Links.Share != "" {
		writeYAMLField(&b, "share_url", meta.Links.Share)
	}
	if meta.Links.Video != "" {
		writeYAMLField(&b, "video_url", meta.Links.Video)
	}

	b.WriteString("---\n\n")

	// Body with info callout
	b.WriteString("# ")
	b.WriteString(coalesce(meta.Title, meta.ID))
	b.WriteString("\n\n")

	// Summary block
	var parts []string
	if meta.Date != "" {
		parts = append(parts, "**Date:** "+dateFromISO(meta.Date))
	}
	if dur := formatDuration(meta.DurationSeconds); dur != "" {
		parts = append(parts, "**Duration:** "+dur)
	}
	if participants := flattenStringSlice(meta.Participants); len(participants) > 0 {
		parts = append(parts, "**Participants:** "+strings.Join(participants, ", "))
	}
	if len(parts) > 0 {
		b.WriteString("> ")
		b.WriteString(strings.Join(parts, " | "))
		b.WriteString("\n")
	}

	// Links
	var links []string
	if meta.Links.Grain != "" {
		links = append(links, fmt.Sprintf("[Grain](%s)", meta.Links.Grain))
	}
	if meta.Links.Share != "" {
		links = append(links, fmt.Sprintf("[Share](%s)", meta.Links.Share))
	}
	if meta.Links.Video != "" {
		links = append(links, fmt.Sprintf("[Video](%s)", meta.Links.Video))
	}
	if len(links) > 0 {
		b.WriteString("\n**Links:** ")
		b.WriteString(strings.Join(links, " · "))
		b.WriteString("\n")
	}

	if notes := formatAny(meta.AINotes); notes != "" {
		b.WriteString("\n## AI Notes\n\n")
		b.WriteString(notes)
		b.WriteString("\n")
	}

	if highlights := formatAny(meta.Highlights); highlights != "" {
		b.WriteString("\n## Highlights\n\n")
		b.WriteString(highlights)
		b.WriteString("\n")
	}

	if transcriptText != "" {
		b.WriteString("\n## Transcript\n\n")
		b.WriteString(transcriptText)
		b.WriteString("\n")
	}

	return b.String()
}

// ── YAML helpers ─────────────────────────────────────────────────────────────

func writeYAMLField(b *strings.Builder, key, value string) {
	if value == "" {
		return
	}
	// Quote values that contain YAML-special characters.
	if needsYAMLQuoting(value) {
		b.WriteString(key)
		b.WriteString(": \"")
		b.WriteString(escapeYAMLString(value))
		b.WriteString("\"\n")
	} else {
		b.WriteString(key)
		b.WriteString(": ")
		b.WriteString(value)
		b.WriteString("\n")
	}
}

func writeYAMLList(b *strings.Builder, key string, items []string) {
	if len(items) == 0 {
		return
	}
	b.WriteString(key)
	b.WriteString(":\n")
	for _, item := range items {
		b.WriteString("  - ")
		if needsYAMLQuoting(item) {
			b.WriteString("\"")
			b.WriteString(escapeYAMLString(item))
			b.WriteString("\"")
		} else {
			b.WriteString(item)
		}
		b.WriteString("\n")
	}
}

func needsYAMLQuoting(s string) bool {
	if s == "" {
		return true
	}
	for _, c := range s {
		switch c {
		case ':', '#', '[', ']', '{', '}', ',', '&', '*', '!', '|', '>', '\'', '"', '%', '@', '`',
			'\n', '\r', '\t':
			return true
		}
	}
	// Quote if starts/ends with whitespace or looks like a number/bool/null.
	if s[0] == ' ' || s[len(s)-1] == ' ' {
		return true
	}
	lower := strings.ToLower(s)
	switch lower {
	case "true", "false", "yes", "no", "null", "~":
		return true
	}
	return false
}

func escapeYAMLString(s string) string {
	s = strings.ReplaceAll(s, `\`, `\\`)
	s = strings.ReplaceAll(s, `"`, `\"`)
	s = strings.ReplaceAll(s, "\n", `\n`)
	s = strings.ReplaceAll(s, "\r", `\r`)
	s = strings.ReplaceAll(s, "\t", `\t`)
	return s
}

// ── Value formatting helpers ─────────────────────────────────────────────────

// flattenStringSlice extracts string values from an any that may be
// []any, []string, or a single string.
func flattenStringSlice(v any) []string {
	if v == nil {
		return nil
	}
	switch val := v.(type) {
	case []string:
		return val
	case string:
		if val == "" {
			return nil
		}
		return []string{val}
	case []any:
		var out []string
		for _, item := range val {
			switch s := item.(type) {
			case string:
				out = append(out, s)
			case map[string]any:
				// Try common keys: "name", "email", "label", "title".
				for _, k := range []string{"name", "email", "label", "title"} {
					if n, ok := s[k].(string); ok && n != "" {
						out = append(out, n)
						break
					}
				}
			}
		}
		return out
	}
	return nil
}

// formatDuration converts a duration (seconds) from any (float64, int, string) to a human string.
func formatDuration(v any) string {
	if v == nil {
		return ""
	}
	var secs float64
	switch d := v.(type) {
	case float64:
		secs = d
	case int:
		secs = float64(d)
	case int64:
		secs = float64(d)
	default:
		return fmt.Sprintf("%v", v)
	}
	if secs <= 0 {
		return ""
	}
	h := int(secs) / 3600
	m := (int(secs) % 3600) / 60
	s := int(secs) % 60
	if h > 0 {
		return fmt.Sprintf("%dh%02dm%02ds", h, m, s)
	}
	if m > 0 {
		return fmt.Sprintf("%dm%02ds", m, s)
	}
	return fmt.Sprintf("%ds", s)
}

// formatAny converts an any value (typically AI notes or highlights) to a string.
func formatAny(v any) string {
	if v == nil {
		return ""
	}
	switch val := v.(type) {
	case string:
		return strings.TrimSpace(val)
	case []any:
		var lines []string
		for _, item := range val {
			switch s := item.(type) {
			case string:
				lines = append(lines, "- "+s)
			case map[string]any:
				if text, ok := s["text"].(string); ok {
					lines = append(lines, "- "+text)
				} else if content, ok := s["content"].(string); ok {
					lines = append(lines, "- "+content)
				}
			}
		}
		return strings.Join(lines, "\n")
	case map[string]any:
		// Try common shapes: {"text": "..."} or {"content": "..."}
		if text, ok := val["text"].(string); ok {
			return strings.TrimSpace(text)
		}
		if content, ok := val["content"].(string); ok {
			return strings.TrimSpace(content)
		}
		// Fall back to sorted key-value listing for stable output.
		keys := make([]string, 0, len(val))
		for k := range val {
			keys = append(keys, k)
		}
		sort.Strings(keys)
		var lines []string
		for _, k := range keys {
			lines = append(lines, fmt.Sprintf("**%s:** %v", k, val[k]))
		}
		return strings.Join(lines, "\n")
	}
	return fmt.Sprintf("%v", v)
}
