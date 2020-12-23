package httpipfs

import (
	"bytes"
	"testing"

	"github.com/sahib/brig/util/testutil"
	"github.com/stretchr/testify/require"
)

func TestGC(t *testing.T) {
	t.Skipf("will be replaced by bash based e2e tests")

	WithIpfs(t, 1, func(t *testing.T, ipfsPath string) {
		nd, err := NewNode(ipfsPath, "")
		require.Nil(t, err)

		data := testutil.CreateDummyBuf(4096 * 1024)
		hash, err := nd.Add(bytes.NewReader(data))
		require.Nil(t, err)

		require.Nil(t, nd.Unpin(hash))
		hashes, err := nd.GC()
		require.Nil(t, err)
		require.True(t, len(hashes) > 0)
	})
}
