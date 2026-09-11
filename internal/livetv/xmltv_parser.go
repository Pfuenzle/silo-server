package livetv

import (
	"context"
	"encoding/json"
	"encoding/xml"
	"fmt"
	"io"
	"sort"
	"strings"
	"time"
)

type xmlTV struct {
	Channels   []xmlTVChannel   `xml:"channel"`
	Programmes []xmlTVProgramme `xml:"programme"`
}
type xmlTVChannel struct {
	ID    string   `xml:"id,attr"`
	Names []string `xml:"display-name"`
}
type xmlTVProgramme struct {
	Channel     string       `xml:"channel,attr"`
	Start       string       `xml:"start,attr"`
	Stop        string       `xml:"stop,attr"`
	Title       string       `xml:"title"`
	Description string       `xml:"desc"`
	Icon        *xmlTVIcon   `xml:"icon"`
	Rating      *xmlTVRating `xml:"rating"`
}
type xmlTVIcon struct {
	URL string `xml:"src,attr"`
}
type xmlTVRating struct {
	Value string `xml:"value"`
}

func ParseXMLTV(ctx context.Context, sourceKey string, input io.Reader, maxBodyBytes int64, mappings map[string]ChannelMapping) (SourceSnapshot, Diagnostics, error) {
	data, err := readBounded(ctx, input, maxBodyBytes)
	if err != nil {
		return SourceSnapshot{}, nil, err
	}
	var feed xmlTV
	if err := xml.Unmarshal(data, &feed); err != nil {
		return SourceSnapshot{}, nil, fmt.Errorf("parse XMLTV: %w", err)
	}
	providerCandidates := make(map[string][]string)
	byName := make(map[string][]string)
	if len(mappings) == 0 {
		for _, channel := range feed.Channels {
			if id := strings.TrimSpace(channel.ID); id != "" {
				providerCandidates[id] = append(providerCandidates[id], id)
			}
		}
	}
	for externalID, mapping := range mappings {
		if mapping.ProviderID != "" {
			providerCandidates[mapping.ProviderID] = append(providerCandidates[mapping.ProviderID], externalID)
		}
		name := strings.ToLower(strings.TrimSpace(mapping.DisplayName))
		if name != "" {
			byName[name] = append(byName[name], externalID)
		}
	}
	var diagnostics Diagnostics
	byProvider := make(map[string]string)
	ambiguousProviders := make(map[string]struct{})
	providerIDs := make([]string, 0, len(providerCandidates))
	for providerID := range providerCandidates {
		providerIDs = append(providerIDs, providerID)
	}
	sort.Strings(providerIDs)
	for _, providerID := range providerIDs {
		candidates := providerCandidates[providerID]
		sort.Strings(candidates)
		if len(candidates) == 1 {
			byProvider[providerID] = candidates[0]
			continue
		}
		ambiguousProviders[providerID] = struct{}{}
		diagnostics = append(diagnostics, Diagnostic{Code: "ambiguous_provider_id", Message: providerID + ": " + strings.Join(candidates, ",")})
	}
	for _, channel := range feed.Channels {
		if len(channel.Names) == 0 {
			continue
		}
		if _, ok := byProvider[channel.ID]; ok {
			continue
		}
		if _, hasProviderID := providerCandidates[channel.ID]; hasProviderID {
			continue
		}
		candidates := byName[strings.ToLower(strings.TrimSpace(channel.Names[0]))]
		if len(candidates) == 1 {
			byProvider[channel.ID] = candidates[0]
		}
		if len(candidates) > 1 {
			diagnostics = append(diagnostics, Diagnostic{Code: "ambiguous_channel", Message: channel.ID})
		}
	}
	snapshot := SourceSnapshot{Programmes: make([]ParsedProgramme, 0)}
	seen := make(map[SourceQualifiedID]struct{})
	for _, item := range feed.Programmes {
		start, startErr := parseXMLTime(item.Start)
		stop, stopErr := parseXMLTime(item.Stop)
		if startErr != nil || stopErr != nil || !stop.After(start) {
			diagnostics = append(diagnostics, Diagnostic{Code: "invalid_timestamp", Message: item.Channel})
			continue
		}
		externalID, ok := byProvider[item.Channel]
		if !ok {
			if _, ambiguous := ambiguousProviders[item.Channel]; ambiguous {
				continue
			}
			diagnostics = append(diagnostics, Diagnostic{Code: "unmapped_channel", Message: item.Channel})
			continue
		}
		programmeID := item.Channel + ":" + item.Start + ":" + item.Title
		stableID, idErr := NewSourceQualifiedID(sourceKey, programmeID)
		if idErr != nil {
			continue
		}
		if _, exists := seen[stableID]; exists {
			diagnostics = append(diagnostics, Diagnostic{Code: "duplicate_id", Message: stableID.String()})
			continue
		}
		seen[stableID] = struct{}{}
		var artwork, rating []byte
		if item.Icon != nil {
			artwork = jsonObject("url", item.Icon.URL)
		}
		if item.Rating != nil {
			rating = jsonObject("value", item.Rating.Value)
		}
		snapshot.Programmes = append(snapshot.Programmes, ParsedProgramme{ChannelExternalID: externalID, Programme: Programme{ExternalID: programmeID, StableID: stableID, Title: strings.TrimSpace(item.Title), Description: strings.TrimSpace(item.Description), StartsAt: start, EndsAt: stop, Artwork: artwork, Rating: rating}})
	}
	return snapshot, diagnostics, nil
}

func parseXMLTime(value string) (time.Time, error) {
	value = strings.TrimSpace(value)
	for _, layout := range []string{"20060102150405 -0700", "20060102150405 MST", time.RFC3339} {
		if parsed, err := time.Parse(layout, value); err == nil {
			return parsed, nil
		}
	}
	return time.Time{}, fmt.Errorf("invalid XMLTV timestamp %q", value)
}

func jsonObject(key, value string) []byte {
	if strings.TrimSpace(value) == "" {
		return nil
	}
	data, _ := json.Marshal(map[string]string{key: value})
	return data
}
