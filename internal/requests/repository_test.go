package requests

import (
	"strings"
	"testing"
)

func TestRequestColumns_omitsFulfillmentColumnsMovedToTargets(t *testing.T) {
	// Given the current media_requests schema stores fulfillment fields on targets.
	columns := requestColumns()

	// When the mine-list projection is built.
	// Then it must not reference columns removed by migration 169.
	for _, removedColumn := range []string{"external_id", "external_status"} {
		if strings.Contains(columns, removedColumn) {
			t.Errorf("request projection references removed column %q: %s", removedColumn, columns)
		}
	}
}
