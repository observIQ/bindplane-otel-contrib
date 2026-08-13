// Copyright observIQ, Inc.
//
// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.
// You may obtain a copy of the License at
//
//     http://www.apache.org/licenses/LICENSE-2.0
//
// Unless required by applicable law or agreed to in writing, software
// distributed under the License is distributed on an "AS IS" BASIS,
// WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
// See the License for the specific language governing permissions and
// limitations under the License.

package awss3eventreceiver_test

import (
	"context"
	"sync"
	"testing"
	"time"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/stretchr/testify/require"
	"go.opentelemetry.io/collector/component/componenttest"
	"go.opentelemetry.io/collector/consumer"
	"go.opentelemetry.io/collector/pdata/plog"
	"go.opentelemetry.io/collector/receiver/receivertest"

	"github.com/observiq/bindplane-otel-contrib/internal/aws/client"
	"github.com/observiq/bindplane-otel-contrib/internal/aws/fake"
	rcvr "github.com/observiq/bindplane-otel-contrib/receiver/awss3eventreceiver"
	"github.com/observiq/bindplane-otel-contrib/receiver/awss3eventreceiver/internal/metadata"
)

// TestShutdownCancelsInflightConsume asserts that Shutdown cancels a worker that is
// blocked inside ConsumeLogs. The worker context must be cancellable; if workers run on
// context.Background(), an in-flight consume never unblocks and Shutdown hangs on
// workerWg.Wait().
func TestShutdownCancelsInflightConsume(t *testing.T) {
	defer fake.SetFakeConstructorForTest(t)()

	ctx := context.Background()
	fakeAWS := client.NewClient(aws.Config{}).(*fake.AWS)
	fakeAWS.CreateObjects(t, map[string]map[string]string{
		"mybucket": {"mykey1": "line1\nline2\n"},
	})

	f := rcvr.NewFactory()
	cfg := f.CreateDefaultConfig().(*rcvr.Config)
	cfg.SQSQueueURL = "https://sqs.us-west-2.amazonaws.com/123456789012/test-queue"
	cfg.StandardPollInterval = 10 * time.Millisecond

	entered := make(chan struct{})
	var once sync.Once
	blocking, err := consumer.NewLogs(func(cctx context.Context, _ plog.Logs) error {
		once.Do(func() { close(entered) })
		<-cctx.Done() // block until the worker context is cancelled by Shutdown
		// Return success so the object acks and the shared fake queue drains, rather
		// than leaking a nacked message into other tests' cleanup assertions.
		return nil
	})
	require.NoError(t, err)

	set := receivertest.NewNopSettings(metadata.Type)
	receiver, err := f.CreateLogs(ctx, set, cfg, blocking)
	require.NoError(t, err)

	host := componenttest.NewNopHost()
	require.NoError(t, receiver.Start(ctx, host))

	select {
	case <-entered:
	case <-time.After(5 * time.Second):
		t.Fatal("worker never reached ConsumeLogs")
	}

	done := make(chan error, 1)
	go func() { done <- receiver.Shutdown(ctx) }()
	select {
	case err := <-done:
		require.NoError(t, err)
	case <-time.After(3 * time.Second):
		t.Fatal("Shutdown hung: the worker context was never cancelled")
	}
}
