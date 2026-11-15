package convert_test

import (
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/alexe0110/most-app/internal/platform/convert"
)

// Every value found in a file is in exactly one non-empty group, the groups come
// in display order, and no key appears twice.
func TestInspect_fieldsAreGroupedInDisplayOrder(t *testing.T) {
	for _, name := range fixtureNames {
		t.Run(name, func(t *testing.T) {
			info, err := convert.Inspect(t.Context(), fixture(t, name))
			require.NoError(t, err)

			keys := map[string]bool{}
			total := 0
			prev := convert.Group(-1)
			for _, g := range info.Groups {
				require.Greater(t, g.Group, prev, "groups are in display order, each once")
				require.NotEmpty(t, g.Fields, "empty groups are left out")
				prev = g.Group
				for _, f := range g.Fields {
					require.False(t, keys[f.Key], "key %q appears twice", f.Key)
					keys[f.Key] = true
					require.NotNil(t, f.Value, f.Key)
					total++
				}
			}
			require.Equal(t, total, info.FieldCount())
			for _, f := range info.SensitiveFields() {
				require.True(t, keys[f.Key])
			}
		})
	}
}

func TestFileInfo_fieldLookupCountAndSensitiveFields(t *testing.T) {
	gps := convert.Field{Key: "GPS:Coordinates", Value: convert.Coordinates{Lat: 1, Lon: 2}, Sensitive: true}
	pages := convert.Field{Key: "PDF:Pages", Value: int64(3)}
	tests := []struct {
		name      string
		groups    []convert.GroupFields
		count     int
		sensitive []string
	}{
		{"nothing found", nil, 0, nil},
		{"places and pages", []convert.GroupFields{
			{Group: convert.GroupLocation, Fields: []convert.Field{gps}},
			{Group: convert.GroupDocument, Fields: []convert.Field{pages}},
		}, 2, []string{"GPS:Coordinates"}},
		{"only open fields", []convert.GroupFields{{Group: convert.GroupDocument, Fields: []convert.Field{pages}}}, 1, nil},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			info := convert.FileInfo{Groups: tt.groups}

			require.Equal(t, tt.count, info.FieldCount())
			var keys []string
			for _, f := range info.SensitiveFields() {
				keys = append(keys, f.Key)
			}
			require.Equal(t, tt.sensitive, keys)
		})
	}
	f, ok := convert.FileInfo{Groups: []convert.GroupFields{{Group: convert.GroupDocument, Fields: []convert.Field{pages}}}}.Field("PDF:Pages")
	require.True(t, ok)
	require.Equal(t, pages, f)
	_, ok = convert.FileInfo{}.Field("PDF:Pages")
	require.False(t, ok)
}
