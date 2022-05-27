/*
Copyright 2021 The Dapr Authors
Licensed under the Apache License, Version 2.0 (the "License");
you may not use this file except in compliance with the License.
You may obtain a copy of the License at
    http://www.apache.org/licenses/LICENSE-2.0
Unless required by applicable law or agreed to in writing, software
distributed under the License is distributed on an "AS IS" BASIS,
WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
See the License for the specific language governing permissions and
limitations under the License.
*/

package pubsub

import (
	"context"
	"fmt"
	"reflect"
	"sort"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/pkg/errors"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/dapr/components-contrib/pubsub"
	"github.com/dapr/components-contrib/tests/conformance/utils"
	"github.com/dapr/kit/config"
)

const (
	defaultPubsubName             = "pubusub"
	defaultTopicName              = "testTopic"
	defaultMultiTopic1Name        = "multiTopic1"
	defaultMultiTopic2Name        = "multiTopic2"
	defaultMessageCount           = 10
	defaultMaxReadDuration        = 60 * time.Second
	defaultWaitDurationToPublish  = 5 * time.Second
	defaultCheckInOrderProcessing = true
)

type TestConfig struct {
	utils.CommonConfig
	PubsubName             string            `mapstructure:"pubsubName"`
	TestTopicName          string            `mapstructure:"testTopicName"`
	TestMultiTopic1Name    string            `mapstructure:"testMultiTopic1Name"`
	TestMultiTopic2Name    string            `mapstructure:"testMultiTopic2Name"`
	PublishMetadata        map[string]string `mapstructure:"publishMetadata"`
	SubscribeMetadata      map[string]string `mapstructure:"subscribeMetadata"`
	MessageCount           int               `mapstructure:"messageCount"`
	MaxReadDuration        time.Duration     `mapstructure:"maxReadDuration"`
	WaitDurationToPublish  time.Duration     `mapstructure:"waitDurationToPublish"`
	CheckInOrderProcessing bool              `mapstructure:"checkInOrderProcessing"`
}

func NewTestConfig(componentName string, allOperations bool, operations []string, configMap map[string]interface{}) (TestConfig, error) {
	// Populate defaults
	tc := TestConfig{
		CommonConfig: utils.CommonConfig{
			ComponentType: "pubsub",
			ComponentName: componentName,
			AllOperations: allOperations,
			Operations:    utils.NewStringSet(operations...),
		},
		PubsubName:             defaultPubsubName,
		TestTopicName:          defaultTopicName,
		TestMultiTopic1Name:    defaultMultiTopic1Name,
		TestMultiTopic2Name:    defaultMultiTopic2Name,
		MessageCount:           defaultMessageCount,
		MaxReadDuration:        defaultMaxReadDuration,
		WaitDurationToPublish:  defaultWaitDurationToPublish,
		PublishMetadata:        map[string]string{},
		SubscribeMetadata:      map[string]string{},
		CheckInOrderProcessing: defaultCheckInOrderProcessing,
	}

	err := config.Decode(configMap, &tc)

	return tc, err
}

func ConformanceTests(t *testing.T, props map[string]string, ps pubsub.PubSub, config TestConfig) {
	// Properly close pubsub
	defer ps.Close()

	actualReadCount := 0

	// Init
	t.Run("init", func(t *testing.T) {
		err := ps.Init(pubsub.Metadata{
			Properties: props,
		})
		assert.NoError(t, err, "expected no error on setting up pubsub")
	})

	// Generate a unique ID for this run to isolate messages to this test
	// and prevent messages still stored in a locally running broker
	// from being considered as part of this test.
	runID := uuid.Must(uuid.NewRandom()).String()
	awaitingMessages := make(map[string]struct{}, 20)
	var mu sync.Mutex
	processedMessages := make(map[int]struct{}, 20)
	processedC := make(chan string, config.MessageCount*2)
	errorCount := 0
	dataPrefix := "message-" + runID + "-"
	var outOfOrder bool
	ctx := context.Background()

	// Subscribe
	if config.HasOperation("subscribe") { // nolint: nestif
		t.Run("subscribe", func(t *testing.T) {
			var counter int
			var lastSequence int
			err := ps.Subscribe(ctx, pubsub.SubscribeRequest{
				Topic:    config.TestTopicName,
				Metadata: config.SubscribeMetadata,
			}, func(ctx context.Context, msg *pubsub.NewMessage) error {
				dataString := string(msg.Data)
				if !strings.HasPrefix(dataString, dataPrefix) {
					t.Logf("Ignoring message without expected prefix")

					return nil
				}

				sequence, err := strconv.Atoi(dataString[len(dataPrefix):])
				if err != nil {
					t.Logf("Message did not contain a sequence number")
					assert.Fail(t, "message did not contain a sequence number")

					return err
				}

				// Ignore already processed messages
				// in case we receive a redelivery from the broker
				// during retries.
				mu.Lock()
				_, alreadyProcessed := processedMessages[sequence]
				mu.Unlock()
				if alreadyProcessed {
					t.Logf("Message was already processed: %d", sequence)

					return nil
				}

				counter++

				if sequence < lastSequence {
					outOfOrder = true
					t.Logf("Message received out of order: expected sequence >= %d, got %d", lastSequence, sequence)
				}

				lastSequence = sequence

				// This behavior is standard to repro a failure of one message in a batch.
				if errorCount < 2 || counter%5 == 0 {
					// First message errors just to give time for more messages to pile up.
					// Second error is to force an error in a batch.
					errorCount++
					// Sleep to allow messages to pile up and be delivered as a batch.
					time.Sleep(1 * time.Second)
					t.Logf("Simulating subscriber error")

					return errors.Errorf("conf test simulated error")
				}

				t.Logf("Simulating subscriber success")
				actualReadCount++

				mu.Lock()
				processedMessages[sequence] = struct{}{}
				mu.Unlock()

				processedC <- dataString

				return nil
			})
			assert.NoError(t, err, "expected no error on subscribe")
		})
	}

	// Publish
	if config.HasOperation("publish") {
		// Some pubsub, like Kafka need to wait for Subscriber to be up before messages can be consumed.
		// So, wait for some time here.
		time.Sleep(config.WaitDurationToPublish)
		t.Run("publish", func(t *testing.T) {
			for k := 1; k <= config.MessageCount; k++ {
				data := []byte(fmt.Sprintf("%s%d", dataPrefix, k))
				err := ps.Publish(&pubsub.PublishRequest{
					Data:       data,
					PubsubName: config.PubsubName,
					Topic:      config.TestTopicName,
					Metadata:   config.PublishMetadata,
				})
				if err == nil {
					awaitingMessages[string(data)] = struct{}{}
				}
				assert.NoError(t, err, "expected no error on publishing data %s on topic %s", data, config.TestTopicName)
			}
		})
	}

	// Verify read
	if config.HasOperation("publish") && config.HasOperation("subscribe") {
		t.Run("verify read", func(t *testing.T) {
			t.Logf("waiting for %v to complete read", config.MaxReadDuration)
			timeout := time.After(config.MaxReadDuration)
			waiting := true
			for waiting {
				select {
				case processed := <-processedC:
					delete(awaitingMessages, processed)
					waiting = len(awaitingMessages) > 0
				case <-timeout:
					// Break out after the mamimum read duration has elapsed
					waiting = false
				}
			}
			assert.False(t, config.CheckInOrderProcessing && outOfOrder, "received messages out of order")
			assert.Empty(t, awaitingMessages, "expected to read %v messages", config.MessageCount)
		})
	}

	// Multiple handlers
	if config.HasOperation("multiplehandlers") {
		received1Ch := make(chan string)
		received2Ch := make(chan string)
		subscribe1Ctx, subscribe1Cancel := context.WithCancel(context.Background())
		subscribe2Ctx, subscribe2Cancel := context.WithCancel(context.Background())
		defer func() {
			subscribe1Cancel()
			subscribe2Cancel()
			close(received1Ch)
			close(received2Ch)
		}()

		t.Run("mutiple handlers", func(t *testing.T) {
			createMultiSubscriber(t, subscribe1Ctx, received1Ch, ps, config.TestMultiTopic1Name, config.SubscribeMetadata, dataPrefix)
			createMultiSubscriber(t, subscribe2Ctx, received2Ch, ps, config.TestMultiTopic2Name, config.SubscribeMetadata, dataPrefix)

			sent1Ch := make(chan string)
			sent2Ch := make(chan string)
			allSentCh := make(chan bool)
			defer func() {
				close(sent1Ch)
				close(sent2Ch)
				close(allSentCh)
			}()
			wait := receiveInBackground(t, config.MaxReadDuration, received1Ch, received2Ch, sent1Ch, sent2Ch, allSentCh)

			for k := (config.MessageCount + 1); k <= (config.MessageCount * 2); k++ {
				data := []byte(fmt.Sprintf("%s%d", dataPrefix, k))
				var topic string
				if k%2 == 0 {
					topic = config.TestMultiTopic1Name
					sent1Ch <- string(data)
				} else {
					topic = config.TestMultiTopic2Name
					sent2Ch <- string(data)
				}
				err := ps.Publish(&pubsub.PublishRequest{
					Data:       data,
					PubsubName: config.PubsubName,
					Topic:      topic,
					Metadata:   config.PublishMetadata,
				})
				assert.NoError(t, err, "expected no error on publishing data %s on topic %s", data, topic)
			}
			allSentCh <- true
			t.Logf("waiting for %v to complete read", config.MaxReadDuration)
			<-wait
		})

		t.Run("stop subscribers", func(t *testing.T) {
			sent1Ch := make(chan string)
			sent2Ch := make(chan string)
			allSentCh := make(chan bool)
			defer func() {
				close(allSentCh)
			}()

			for i := 0; i < 3; i++ {
				switch i {
				case 1: // On iteration 1, close the first subscriber
					subscribe1Cancel()
					close(sent1Ch)
					sent1Ch = nil
					time.Sleep(config.WaitDurationToPublish)
				case 2: // On iteration 1, close the second subscriber
					subscribe2Cancel()
					close(sent2Ch)
					sent2Ch = nil
					time.Sleep(config.WaitDurationToPublish)
				}

				wait := receiveInBackground(t, config.MaxReadDuration, received1Ch, received2Ch, sent1Ch, sent2Ch, allSentCh)

				offset := config.MessageCount * (i + 2)
				for k := offset + 1; k <= (offset + config.MessageCount); k++ {
					data := []byte(fmt.Sprintf("%s%d", dataPrefix, k))
					var topic string
					if k%2 == 0 {
						topic = config.TestMultiTopic1Name
						if sent1Ch != nil {
							sent1Ch <- string(data)
						}
					} else {
						topic = config.TestMultiTopic2Name
						if sent2Ch != nil {
							sent2Ch <- string(data)
						}
					}
					err := ps.Publish(&pubsub.PublishRequest{
						Data:       data,
						PubsubName: config.PubsubName,
						Topic:      topic,
						Metadata:   config.PublishMetadata,
					})
					assert.NoError(t, err, "expected no error on publishing data %s on topic %s", data, topic)
				}

				allSentCh <- true
				t.Logf("waiting for %v to complete read", config.MaxReadDuration)
				<-wait
			}
		})
	}
}

func receiveInBackground(t *testing.T, timeout time.Duration, received1Ch <-chan string, received2Ch <-chan string, sent1Ch <-chan string, sent2Ch <-chan string, allSentCh <-chan bool) <-chan struct{} {
	done := make(chan struct{})

	go func() {
		receivedTopic1 := make([]string, 0)
		expectedTopic1 := make([]string, 0)
		receivedTopic2 := make([]string, 0)
		expectedTopic2 := make([]string, 0)
		to := time.NewTimer(timeout)
		allSent := false

		defer func() {
			to.Stop()
			close(done)
		}()

		for {
			select {
			case msg := <-received1Ch:
				receivedTopic1 = append(receivedTopic1, msg)
			case msg := <-received2Ch:
				receivedTopic2 = append(receivedTopic2, msg)
			case msg := <-sent1Ch:
				expectedTopic1 = append(expectedTopic1, msg)
			case msg := <-sent2Ch:
				expectedTopic2 = append(expectedTopic2, msg)
			case v := <-allSentCh:
				allSent = v
			case <-to.C:
				assert.Failf(t, "timeout while waiting for messages in multihandlers", "receivedTopic1=%v receivedTopic2=%v", receivedTopic1, receivedTopic2)
				return
			}

			if allSent && compareReceivedAndExpected(receivedTopic1, expectedTopic1) && compareReceivedAndExpected(receivedTopic2, expectedTopic2) {
				return
			}
		}
	}()

	return done
}

func compareReceivedAndExpected(received []string, expected []string) bool {
	sort.Strings(received)
	sort.Strings(expected)
	return reflect.DeepEqual(received, expected)
}

func createMultiSubscriber(t *testing.T, subscribeCtx context.Context, ch chan<- string, ps pubsub.PubSub, topic string, subscribeMetadata map[string]string, dataPrefix string) {
	err := ps.Subscribe(subscribeCtx, pubsub.SubscribeRequest{
		Topic:    topic,
		Metadata: subscribeMetadata,
	}, func(ctx context.Context, msg *pubsub.NewMessage) error {
		dataString := string(msg.Data)
		if !strings.HasPrefix(dataString, dataPrefix) {
			t.Log("Ignoring message without expected prefix", dataString)
			return nil
		}
		ch <- string(msg.Data)
		return nil
	})
	require.NoError(t, err, "expected no error on subscribe")
}
