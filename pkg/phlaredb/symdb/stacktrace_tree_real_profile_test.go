package symdb

import (
	"bytes"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/grafana/pyroscope/v2/pkg/pprof"
)

// insertOriginal preserves the insertion algorithm before wide-node indexing,
// including appending (rather than prepending) siblings.
func (t *stacktraceTree) insertOriginal(refs []uint64) uint32 {
	n := &t.nodes[0]
	i := n.fc
	var x int32
	for j := len(refs) - 1; j >= 0; {
		r := int32(refs[j])
		if i == sentinel {
			ni := int32(len(t.nodes))
			n.fc = ni
			t.nodes = append(t.nodes, node{r: r, p: x, fc: sentinel, ns: sentinel})
			x = ni
			n = &t.nodes[ni]
		} else {
			x = i
			n = &t.nodes[i]
		}
		if n.r == r {
			i = n.fc
			j--
			continue
		}
		if n.ns < 0 {
			n.ns = int32(len(t.nodes))
			t.nodes = append(t.nodes, node{r: r, p: n.p, fc: sentinel, ns: sentinel})
		}
		i = n.ns
	}
	return uint32(x)
}

func Test_stacktrace_tree_big_profile_compatibility(t *testing.T) {
	p, err := pprof.OpenFile("testdata/big-profile.pb.gz")
	require.NoError(t, err)
	original := newStacktraceTree(defaultStacktraceTreeSize)
	indexed := newStacktraceTree(defaultStacktraceTreeSize)
	for _, s := range p.Sample {
		require.Equal(t, original.insertOriginal(s.LocationId), indexed.insert(s.LocationId))
	}
	require.Equal(t, original.nodes, indexed.nodes)
	var before, after bytes.Buffer
	_, err = original.WriteTo(&before)
	require.NoError(t, err)
	_, err = indexed.WriteTo(&after)
	require.NoError(t, err)
	require.Equal(t, before.Bytes(), after.Bytes())
	t.Logf("samples=%d nodes=%d indexed children=%d index bytes (estimated)=%d", len(p.Sample), len(indexed.nodes), len(indexed.wideChildren), indexed.wideIndexSize())
}

func Benchmark_stacktrace_tree_big_profile(b *testing.B) {
	p, err := pprof.OpenFile("testdata/big-profile.pb.gz")
	require.NoError(b, err)
	for _, variant := range []struct {
		name   string
		insert func(*stacktraceTree, []uint64) uint32
	}{
		{"original", (*stacktraceTree).insertOriginal},
		{"scan_only", (*stacktraceTree).insertScanOnly},
		{"index_disabled", func(t *stacktraceTree, refs []uint64) uint32 {
			t.wideThreshold = 0
			return t.insert(refs)
		}},
		{"indexed", (*stacktraceTree).insert},
	} {
		b.Run(variant.name, func(b *testing.B) {
			b.ReportAllocs()
			for i := 0; i < b.N; i++ {
				x := newStacktraceTree(defaultStacktraceTreeSize)
				for _, s := range p.Sample {
					variant.insert(x, s.LocationId)
				}
			}
		})
	}
}
