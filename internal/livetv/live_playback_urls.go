package livetv

import (
	"fmt"
	"net/url"
	"path"
	"strings"
)

func (s *LivePlaybackService) resourceURL(session *LivePlaybackSession, resource string) (string, error) {
	if resource == "" {
		return session.providerURL, nil
	}
	if providerURL, ok := session.resources[resource]; ok {
		return providerURL, nil
	}
	return "", ErrSourcePolicy
}

func parseLiveRoute(rawPath string) (string, string, string, bool) {
	parts := strings.Split(strings.Trim(rawPath, "/"), "/")
	if len(parts) < 4 || parts[0] != "stream" || parts[1] != "live" || parts[2] == "" {
		return "", "", "", false
	}
	if parts[3] == "manifest" {
		return parts[2], "manifest", "", true
	}
	if len(parts) == 5 && parts[3] == "segment" && parts[4] != "" {
		return parts[2], "segment", parts[4], true
	}
	return "", "", "", false
}

func resolveLiveResource(base, resource string) (string, error) {
	baseURL, err := url.Parse(base)
	if err != nil {
		return "", ErrSourcePolicy
	}
	if resource == "" {
		return baseURL.String(), nil
	}
	resolved, err := url.Parse(resource)
	if err != nil || strings.Contains(resource, "..") {
		return "", ErrSourcePolicy
	}
	if resolved.IsAbs() || resolved.Host != "" {
		if (resolved.Scheme != "http" && resolved.Scheme != "https") || resolved.Host != baseURL.Host {
			return "", ErrSourcePolicy
		}
		return resolved.String(), nil
	}
	baseURL.Path = path.Join(path.Dir(baseURL.Path), resolved.Path)
	baseURL.RawQuery = resolved.RawQuery
	return baseURL.String(), nil
}

func (s *LivePlaybackService) rewriteLiveManifest(session *LivePlaybackSession, body, origin string) string {
	lines := strings.Split(body, "\n")
	for index, line := range lines {
		trimmed := strings.TrimSpace(line)
		if trimmed == "" {
			continue
		}
		if strings.HasPrefix(trimmed, "#") {
			lines[index] = s.rewriteLiveURIAttribute(session, line, origin)
			continue
		}
		if len(session.resources) >= 4096 {
			lines[index] = ""
			continue
		}
		resourceID := fmt.Sprintf("r-%d", len(session.resources)+1)
		providerURL, err := resolveLiveResource(session.providerURL, trimmed)
		if err != nil {
			lines[index] = ""
			continue
		}
		session.resources[resourceID] = providerURL
		lines[index] = origin + "/stream/live/" + session.GrantID + "/segment/" + url.PathEscape(resourceID)
	}
	return strings.Join(lines, "\n")
}

func (s *LivePlaybackService) rewriteLiveURIAttribute(session *LivePlaybackSession, line, origin string) string {
	start := strings.Index(strings.ToUpper(line), "URI=")
	if start < 0 {
		return line
	}
	valueStart := start + len("URI=")
	if valueStart >= len(line) {
		return line[:start]
	}
	quote := byte(0)
	if line[valueStart] == '"' || line[valueStart] == '\'' {
		quote = line[valueStart]
		valueStart++
	}
	valueEnd := valueStart
	for valueEnd < len(line) && (quote != 0 && line[valueEnd] != quote || quote == 0 && line[valueEnd] != ',' && line[valueEnd] != '\r' && line[valueEnd] != '\n') {
		valueEnd++
	}
	if valueEnd == valueStart {
		return line[:start]
	}
	providerURL, err := resolveLiveResource(session.providerURL, line[valueStart:valueEnd])
	if err != nil {
		return line[:start] + line[valueEnd:]
	}
	if len(session.resources) >= 4096 {
		return line[:start] + line[valueEnd:]
	}
	resourceID := fmt.Sprintf("r-%d", len(session.resources)+1)
	session.resources[resourceID] = providerURL
	replacement := origin + "/stream/live/" + session.GrantID + "/segment/" + url.PathEscape(resourceID)
	return line[:start] + "URI=\"" + replacement + "\"" + line[valueEnd:]
}
