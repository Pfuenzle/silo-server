package livetv

import (
	"bufio"
	"context"
	"errors"
	"html"
	"io"
	"strings"
)

var ErrBodyTooLarge = errors.New("Live TV source body exceeds configured limit")

type Diagnostic struct {
	Code    string
	Message string
}

type Diagnostics []Diagnostic

func (d Diagnostics) HasCode(code string) bool {
	for _, item := range d {
		if item.Code == code {
			return true
		}
	}
	return false
}

type SourceSnapshot struct {
	Channels   []Channel
	Programmes []ParsedProgramme
}

type ParsedProgramme struct {
	ChannelExternalID string
	Programme
}

type ChannelMapping struct {
	ProviderID  string
	DisplayName string
}

func ParseM3U(ctx context.Context, sourceKey string, input io.Reader, maxBodyBytes int64) (SourceSnapshot, Diagnostics, error) {
	data, err := readBounded(ctx, input, maxBodyBytes)
	if err != nil {
		return SourceSnapshot{}, nil, err
	}
	snapshot := SourceSnapshot{Channels: make([]Channel, 0)}
	var diagnostics Diagnostics
	scanner := bufio.NewScanner(strings.NewReader(string(data)))
	scanner.Buffer(make([]byte, 1024), 1024*1024)
	var pending string
	seen := make(map[SourceQualifiedID]struct{})
	for scanner.Scan() {
		if err := ctx.Err(); err != nil {
			return SourceSnapshot{}, diagnostics, err
		}
		line := strings.TrimSpace(scanner.Text())
		if strings.HasPrefix(line, "#EXTINF:") {
			if pending != "" {
				diagnostics = append(diagnostics, Diagnostic{Code: "missing_stream_url", Message: pending})
			}
			pending = line
			continue
		}
		if pending == "" || strings.HasPrefix(line, "#") || line == "" {
			continue
		}
		channel, parseErr := parseM3UEntry(sourceKey, pending, line)
		pending = ""
		if parseErr != nil {
			diagnostics = append(diagnostics, Diagnostic{Code: parseErr.code, Message: parseErr.message})
			continue
		}
		if _, exists := seen[channel.StableID]; exists {
			diagnostics = append(diagnostics, Diagnostic{Code: "duplicate_id", Message: channel.StableID.String()})
			continue
		}
		seen[channel.StableID] = struct{}{}
		snapshot.Channels = append(snapshot.Channels, channel)
	}
	if err := scanner.Err(); err != nil {
		return SourceSnapshot{}, diagnostics, err
	}
	if pending != "" {
		diagnostics = append(diagnostics, Diagnostic{Code: "missing_stream_url", Message: pending})
	}
	return snapshot, diagnostics, nil
}

type parseDiagnostic struct{ code, message string }

func parseM3UEntry(sourceKey, metadata, streamURL string) (Channel, *parseDiagnostic) {
	parts := strings.SplitN(metadata, ",", 2)
	if len(parts) != 2 {
		return Channel{}, &parseDiagnostic{"malformed_entry", "missing channel name"}
	}
	attrs := parseAttributes(parts[0])
	id := strings.TrimSpace(attrs["tvg-id"])
	if id == "" {
		return Channel{}, &parseDiagnostic{"missing_id", "channel has no tvg-id"}
	}
	streamURL = strings.TrimSpace(streamURL)
	if streamURL == "" {
		return Channel{}, &parseDiagnostic{"missing_stream_url", id}
	}
	stableID, err := NewSourceQualifiedID(sourceKey, id)
	if err != nil {
		return Channel{}, &parseDiagnostic{"invalid_id", id}
	}
	return Channel{ExternalID: id, StableID: stableID, Name: strings.TrimSpace(parts[1]), Number: strings.TrimSpace(attrs["tvg-chno"]), Category: ChannelCategory(attrs["group-title"]), StreamURL: streamURL, Artwork: jsonObject("logo", attrs["tvg-logo"])}, nil
}

func parseAttributes(text string) map[string]string {
	attrs := make(map[string]string)
	for len(text) > 0 {
		start := strings.IndexByte(text, ' ')
		if start < 0 {
			break
		}
		text = strings.TrimSpace(text[start:])
		eq := strings.IndexByte(text, '=')
		if eq < 0 {
			break
		}
		key := strings.TrimSpace(text[:eq])
		text = strings.TrimSpace(text[eq+1:])
		if len(text) == 0 || text[0] != '"' {
			break
		}
		end := quotedAttributeEnd(text[1:])
		if end < 0 {
			break
		}
		attrs[key] = strings.ReplaceAll(html.UnescapeString(text[1:end+1]), `\"`, `"`)
		text = text[end+2:]
	}
	return attrs
}

func quotedAttributeEnd(value string) int {
	for index := 0; index < len(value); index++ {
		if value[index] != '"' || (index > 0 && value[index-1] == '\\') {
			continue
		}
		return index
	}
	return -1
}

func readBounded(ctx context.Context, input io.Reader, maxBodyBytes int64) ([]byte, error) {
	if maxBodyBytes <= 0 {
		return nil, ErrBodyTooLarge
	}
	reader := &contextReader{ctx: ctx, reader: io.LimitReader(input, maxBodyBytes+1)}
	data, err := io.ReadAll(reader)
	if err != nil {
		return nil, err
	}
	if int64(len(data)) > maxBodyBytes {
		return nil, ErrBodyTooLarge
	}
	return data, nil
}

type contextReader struct {
	ctx    context.Context
	reader io.Reader
}

func (r *contextReader) Read(p []byte) (int, error) {
	if err := r.ctx.Err(); err != nil {
		return 0, err
	}
	return r.reader.Read(p)
}
