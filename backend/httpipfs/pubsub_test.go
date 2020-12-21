package httpipfs

import (
	"context"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

func TestPubSub(t *testing.T) {
	t.Skipf("will be replaced by bash based e2e tests")

	// Only use one ipfs instance, for test performance.
	WithIpfs(t, 1, func(t *testing.T, ipfsPath string) {
		nd, err := NewNode(ipfsPath, "")
		require.Nil(t, err)

		self, err := nd.Identity()
		require.Nil(t, err)

		ctx := context.Background()
		sub, err := nd.Subscribe(ctx, "test-topic")
		require.Nil(t, err)

		defer func() {
			require.Nil(t, sub.Close())
		}()

		time.Sleep(1 * time.Second)
		data := []byte("hello world!")
		go nd.PublishEvent("test-topic", data)

		msg, err := sub.Next(ctx)
		require.Nil(t, err)

		require.Equal(t, data, msg.Data())
		require.Equal(t, self.Addr, msg.Source())
	})
}
