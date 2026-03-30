package googlesecopsexporter

import (
	"testing"

	"github.com/observiq/bindplane-otel-contrib/exporter/googlesecopsexporter/protos/api"
	"github.com/stretchr/testify/require"
)

func TestCreateGroupKey(t *testing.T) {
	tests := []struct {
		name            string
		namespace       string
		logType         string
		ingestionLabels []*api.Label
		expected        string
	}{
		{
			name:            "no labels",
			namespace:       "ns1",
			logType:         "WINEVTLOG",
			ingestionLabels: nil,
			expected:        "ns1|WINEVTLOG",
		},
		{
			name:      "with labels",
			namespace: "ns1",
			logType:   "WINEVTLOG",
			ingestionLabels: []*api.Label{
				{Key: "env", Value: "prod"},
				{Key: "app", Value: "web"},
			},
			expected: "ns1|WINEVTLOG|app=web,env=prod",
		},
		{
			name:      "labels are sorted",
			namespace: "ns1",
			logType:   "WINEVTLOG",
			ingestionLabels: []*api.Label{
				{Key: "z", Value: "last"},
				{Key: "a", Value: "first"},
			},
			expected: "ns1|WINEVTLOG|a=first,z=last",
		},
		{
			name:            "empty namespace and logType",
			namespace:       "",
			logType:         "",
			ingestionLabels: nil,
			expected:        "|",
		},
		{
			name:            "empty labels slice",
			namespace:       "ns1",
			logType:         "SYSLOG",
			ingestionLabels: []*api.Label{},
			expected:        "ns1|SYSLOG",
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got := createGroupKey(tc.namespace, tc.logType, tc.ingestionLabels)
			require.Equal(t, tc.expected, got)
		})
	}
}

func TestLogGrouper_Add(t *testing.T) {
	t.Run("adds to new group", func(t *testing.T) {
		g := newLogGrouper()
		entry := &api.LogEntry{Data: []byte("log1")}
		g.Add(entry, "ns1", "WINEVTLOG", nil)

		require.Len(t, g.groups, 1)
		key := createGroupKey("ns1", "WINEVTLOG", nil)
		group := g.groups[key]
		require.NotNil(t, group)
		require.Equal(t, "ns1", group.namespace)
		require.Equal(t, "WINEVTLOG", group.logType)
		require.Nil(t, group.ingestionLabels)
		require.Len(t, group.entries, 1)
		require.Equal(t, entry, group.entries[0])
	})

	t.Run("appends to existing group", func(t *testing.T) {
		g := newLogGrouper()
		entry1 := &api.LogEntry{Data: []byte("log1")}
		entry2 := &api.LogEntry{Data: []byte("log2")}

		g.Add(entry1, "ns1", "WINEVTLOG", nil)
		g.Add(entry2, "ns1", "WINEVTLOG", nil)

		require.Len(t, g.groups, 1)
		key := createGroupKey("ns1", "WINEVTLOG", nil)
		require.Len(t, g.groups[key].entries, 2)
		require.Equal(t, entry1, g.groups[key].entries[0])
		require.Equal(t, entry2, g.groups[key].entries[1])
	})

	t.Run("different logTypes create separate groups", func(t *testing.T) {
		g := newLogGrouper()
		g.Add(&api.LogEntry{Data: []byte("log1")}, "ns1", "WINEVTLOG", nil)
		g.Add(&api.LogEntry{Data: []byte("log2")}, "ns1", "SYSLOG", nil)

		require.Len(t, g.groups, 2)
	})

	t.Run("different namespaces create separate groups", func(t *testing.T) {
		g := newLogGrouper()
		g.Add(&api.LogEntry{Data: []byte("log1")}, "ns1", "WINEVTLOG", nil)
		g.Add(&api.LogEntry{Data: []byte("log2")}, "ns2", "WINEVTLOG", nil)

		require.Len(t, g.groups, 2)
	})

	t.Run("different labels create separate groups", func(t *testing.T) {
		g := newLogGrouper()
		labels1 := []*api.Label{{Key: "env", Value: "prod"}}
		labels2 := []*api.Label{{Key: "env", Value: "staging"}}

		g.Add(&api.LogEntry{Data: []byte("log1")}, "ns1", "WINEVTLOG", labels1)
		g.Add(&api.LogEntry{Data: []byte("log2")}, "ns1", "WINEVTLOG", labels2)

		require.Len(t, g.groups, 2)
	})

	t.Run("same labels in different order map to same group", func(t *testing.T) {
		g := newLogGrouper()
		labels1 := []*api.Label{
			{Key: "env", Value: "prod"},
			{Key: "app", Value: "web"},
		}
		labels2 := []*api.Label{
			{Key: "app", Value: "web"},
			{Key: "env", Value: "prod"},
		}

		g.Add(&api.LogEntry{Data: []byte("log1")}, "ns1", "WINEVTLOG", labels1)
		g.Add(&api.LogEntry{Data: []byte("log2")}, "ns1", "WINEVTLOG", labels2)

		require.Len(t, g.groups, 1)
	})
}

func TestLogGrouper_ForEach(t *testing.T) {
	t.Run("iterates over all groups", func(t *testing.T) {
		g := newLogGrouper()
		g.Add(&api.LogEntry{Data: []byte("log1")}, "ns1", "WINEVTLOG", nil)
		g.Add(&api.LogEntry{Data: []byte("log2")}, "ns2", "SYSLOG", nil)

		visited := make(map[string]bool)
		g.ForEach(func(entries []*api.LogEntry, namespace, logType string, ingestionLabels []*api.Label) {
			key := createGroupKey(namespace, logType, ingestionLabels)
			visited[key] = true
		})

		require.Len(t, visited, 2)
		require.True(t, visited["ns1|WINEVTLOG"])
		require.True(t, visited["ns2|SYSLOG"])
	})

	t.Run("passes correct entries to callback", func(t *testing.T) {
		g := newLogGrouper()
		entry1 := &api.LogEntry{Data: []byte("log1")}
		entry2 := &api.LogEntry{Data: []byte("log2")}
		g.Add(entry1, "ns1", "WINEVTLOG", nil)
		g.Add(entry2, "ns1", "WINEVTLOG", nil)

		g.ForEach(func(entries []*api.LogEntry, namespace, logType string, ingestionLabels []*api.Label) {
			require.Len(t, entries, 2)
			require.Equal(t, entry1, entries[0])
			require.Equal(t, entry2, entries[1])
		})
	})

	t.Run("no-op on empty grouper", func(t *testing.T) {
		g := newLogGrouper()
		called := false
		g.ForEach(func(entries []*api.LogEntry, namespace, logType string, ingestionLabels []*api.Label) {
			called = true
		})
		require.False(t, called)
	})
}

func TestLogGrouper_Clear(t *testing.T) {
	g := newLogGrouper()
	g.Add(&api.LogEntry{Data: []byte("log1")}, "ns1", "WINEVTLOG", nil)
	g.Add(&api.LogEntry{Data: []byte("log2")}, "ns2", "SYSLOG", nil)

	require.Len(t, g.groups, 2)

	g.Clear()

	require.Len(t, g.groups, 0)

	// Verify grouper is still usable after clear
	g.Add(&api.LogEntry{Data: []byte("log3")}, "ns3", "OKTA", nil)
	require.Len(t, g.groups, 1)
}
