package main

import (
	"context"
	"testing"

	"github.com/getsentry/sentry-go"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/watch"
)

func TestHandlePodWatchEvent(t *testing.T) {

	ctx := context.Background()

	transport := &TransportMock{}
	client, err := sentry.NewClient(sentry.ClientOptions{
		Transport: transport,
		Integrations: func([]sentry.Integration) []sentry.Integration {
			return []sentry.Integration{}
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	scope := sentry.NewScope()

	hub := sentry.NewHub(client, scope)

	ctx = sentry.SetHubOnContext(ctx, hub)

	// create the event which includes the mock pod
	mockEvent := watch.Event{
		Type: watch.Modified,
		Object: &corev1.Pod{
			ObjectMeta: metav1.ObjectMeta{
				Name:      "TestHandlePodWatchEventPod",
				Namespace: "TestHandlePodWatchEventNameSpace",
			},
			Status: corev1.PodStatus{
				ContainerStatuses: []corev1.ContainerStatus{
					{
						Name: "fake_DNS_Label",
						State: corev1.ContainerState{
							Terminated: &corev1.ContainerStateTerminated{
								ExitCode: 1,
								Reason:   "Fake Reason: TestHandlePodWatchEvent",
								Message:  "Fake Message: TestHandlePodWatchEvent",
							},
						},
					},
				},
			},
		},
	}

	// process the events
	handlePodWatchEvent(ctx, &mockEvent)

	expectedNumEvents := 1
	expectedMsg := "Fake Message: TestHandlePodWatchEvent"

	events := transport.Events()
	if len(events) != 1 {
		t.Errorf("received %d events, expected %d event", len(events), expectedNumEvents)
	}

	if events[0].Message != expectedMsg {
		t.Errorf("received %s, wanted %s", events[0].Message, expectedMsg)
	}
}
