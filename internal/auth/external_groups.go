package auth

import (
	"errors"
	"sort"
	"unicode/utf8"

	"google.golang.org/protobuf/proto"
	"google.golang.org/protobuf/types/known/structpb"

	"github.com/Silo-Server/silo-server/internal/externalgroups"
)

const (
	externalGroupsVersionKey  = "silo.external_groups.v1"
	externalGroupsProvider    = "ldap"
	maxExternalGroups         = 100
	maxExternalGroupIDBytes   = externalgroups.MaxIDBytes
	maxExternalGroupNameBytes = 256
	maxExternalGroupsBytes    = 32 << 10
)

var ErrExternalGroupsInvalid = errors.New("external groups claims are invalid")

// ExternalGroup is a stable directory group identifier and optional display label.
// Only ID participates in authorization decisions.
type ExternalGroup struct {
	ID          string
	DisplayName string
}

// ParseExternalGroupsV1 accepts only the exact versioned LDAP claims envelope.
func ParseExternalGroupsV1(claims *structpb.Struct) ([]ExternalGroup, error) {
	if claims == nil || proto.Size(claims) > maxExternalGroupsBytes {
		return nil, ErrExternalGroupsInvalid
	}
	fields := claims.GetFields()
	if len(fields) != 1 {
		return nil, ErrExternalGroupsInvalid
	}
	envelope, ok := structValue(fields[externalGroupsVersionKey])
	if !ok {
		return nil, ErrExternalGroupsInvalid
	}
	return parseExternalGroupsEnvelope(envelope)
}

func parseExternalGroupsEnvelope(envelope *structpb.Struct) ([]ExternalGroup, error) {
	fields := envelope.GetFields()
	if len(fields) != 2 {
		return nil, ErrExternalGroupsInvalid
	}
	provider, ok := stringValue(fields["provider_kind"])
	if !ok || provider != externalGroupsProvider {
		return nil, ErrExternalGroupsInvalid
	}
	list, ok := listValue(fields["groups"])
	if !ok || len(list.GetValues()) > maxExternalGroups {
		return nil, ErrExternalGroupsInvalid
	}
	groups := make([]ExternalGroup, 0, len(list.GetValues()))
	seen := make(map[string]struct{}, len(list.GetValues()))
	for _, value := range list.GetValues() {
		group, err := parseExternalGroup(value)
		if err != nil {
			return nil, err
		}
		if _, exists := seen[group.ID]; exists {
			return nil, ErrExternalGroupsInvalid
		}
		seen[group.ID] = struct{}{}
		groups = append(groups, group)
	}
	sort.Slice(groups, func(i, j int) bool { return groups[i].ID < groups[j].ID })
	return groups, nil
}

func parseExternalGroup(value *structpb.Value) (ExternalGroup, error) {
	group, ok := structValue(value)
	if !ok {
		return ExternalGroup{}, ErrExternalGroupsInvalid
	}
	fields := group.GetFields()
	if len(fields) == 0 || len(fields) > 2 {
		return ExternalGroup{}, ErrExternalGroupsInvalid
	}
	rawID, ok := stringValue(fields["id"])
	if !ok {
		return ExternalGroup{}, ErrExternalGroupsInvalid
	}
	id, err := externalgroups.NormalizeID(rawID)
	if err != nil {
		return ExternalGroup{}, ErrExternalGroupsInvalid
	}
	result := ExternalGroup{ID: id}
	if display, exists := fields["display_name"]; exists {
		name, stringOK := stringValue(display)
		if !stringOK || !validExternalGroupString(name, maxExternalGroupNameBytes) {
			return ExternalGroup{}, ErrExternalGroupsInvalid
		}
		result.DisplayName = name
	}
	for key := range fields {
		if key != "id" && key != "display_name" {
			return ExternalGroup{}, ErrExternalGroupsInvalid
		}
	}
	return result, nil
}

func validExternalGroupString(value string, maxBytes int) bool {
	return value != "" && len(value) <= maxBytes && utf8.ValidString(value)
}

func structValue(value *structpb.Value) (*structpb.Struct, bool) {
	if value == nil {
		return nil, false
	}
	result := value.GetStructValue()
	return result, result != nil
}

func listValue(value *structpb.Value) (*structpb.ListValue, bool) {
	if value == nil {
		return nil, false
	}
	result := value.GetListValue()
	return result, result != nil
}

func stringValue(value *structpb.Value) (string, bool) {
	if value == nil || value.GetKind() == nil {
		return "", false
	}
	result, ok := value.GetKind().(*structpb.Value_StringValue)
	if !ok {
		return "", false
	}
	return result.StringValue, true
}
