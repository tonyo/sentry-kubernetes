package main

import (
	"context"
	"fmt"

	"github.com/getsentry/sentry-go"
	"github.com/rs/zerolog"
	batchv1 "k8s.io/api/batch/v1"
	v1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
)

const breadcrumbLimit = 20

func runPodEnhancer(ctx context.Context, podMeta *v1.ObjectReference, cachedObject interface{}, scope *sentry.Scope, sentryEvent *sentry.Event) error {
	logger := zerolog.Ctx(ctx)

	logger.Debug().Msgf("Running the pod enhancer")

	clientset, err := getClientsetFromContext(ctx)
	if err != nil {
		return err
	}

	namespace := podMeta.Namespace
	podName := podMeta.Name
	opts := metav1.GetOptions{}

	cachedPod, _ := cachedObject.(*v1.Pod)
	var pod *v1.Pod
	if cachedPod == nil {
		logger.Debug().Msgf("Fetching pod data")
		// FIXME: this can probably be cached if we use NewSharedInformerFactory
		pod, err = clientset.CoreV1().Pods(namespace).Get(context.Background(), podName, opts)
		if err != nil {
			return err
		}
	} else {
		logger.Debug().Msgf("Reusing the available pod object")
		pod = cachedPod
	}

	// Clean-up the object
	pod.ManagedFields = []metav1.ManagedFieldsEntry{}

	nodeName := pod.Spec.NodeName
	setTagIfNotEmpty(scope, "node_name", nodeName)

	metadataJson, err := prettyJson(pod.ObjectMeta)
	if err == nil {
		scope.SetExtra("Pod Metadata", metadataJson)
	}

	// The data will be mostly duplicated in "Pod Metadata"
	scope.RemoveExtra("Involved Object")

	// Add related events as breadcrumbs
	podEvents := filterEventsFromBuffer(namespace, "Pod", podName)
	for _, podEvent := range podEvents {
		breadcrumbLevel := sentry.LevelInfo
		if podEvent.Type == v1.EventTypeWarning {
			breadcrumbLevel = sentry.LevelWarning
		}

		scope.AddBreadcrumb(&sentry.Breadcrumb{
			Message:   podEvent.Message,
			Level:     breadcrumbLevel,
			Timestamp: podEvent.LastTimestamp.Time,
		}, breadcrumbLimit)
	}

	message := sentryEvent.Message
	sentryEvent.Message = fmt.Sprintf("%s: %s", podName, sentryEvent.Message)

	logger.Trace().Msgf("Current fingerprint: %v", sentryEvent.Fingerprint)

	// Adjust fingerprint.
	// If there's already a non-empty fingerprint set, we assume that it was set by
	// another enhancer, so we don't touch it.
	if len(sentryEvent.Fingerprint) == 0 {
		sentryEvent.Fingerprint = []string{message}
	}

	// using finger print to group events together

	// first attempt to group events by cronJobs
	var owningCronJob *batchv1.CronJob = nil

	// check if the pod corresponds to a cronJob
	for _, podRef := range pod.ObjectMeta.OwnerReferences {
		// check the pod has a job as an owner
		if !*podRef.Controller || podRef.Kind != "Job" {
			continue
		}
		// find the owning job
		owningJob, err := clientset.BatchV1().Jobs(namespace).Get(context.Background(), podRef.Name, opts)
		if err != nil {
			continue
		}
		// check if owning job is owned by a cronJob
		for _, jobRef := range owningJob.ObjectMeta.OwnerReferences {
			if !*jobRef.Controller || jobRef.Kind != "CronJob" {
				continue
			}
			owningCronJob, err = clientset.BatchV1().CronJobs(namespace).Get(context.Background(), jobRef.Name, opts)
			if err != nil {
				continue
			}
		}
	}

	// the pod is not owned by a higher resource
	if len(pod.OwnerReferences) == 0 {
		// Standalone pod => most probably it has a unique name
		sentryEvent.Fingerprint = append(sentryEvent.Fingerprint, podName)
		// the pod is owned by a higher resource
	} else {
		// the pod is owned by a cronJob (as grandchild)
		if owningCronJob != nil {
			fmt.Printf("set fingerprint, breadcrumb, and metadata for cronJob %s\n", owningCronJob.Name)
			sentryEvent.Fingerprint = append(sentryEvent.Fingerprint, owningCronJob.Kind, owningCronJob.Name)
			setTagIfNotEmpty(scope, "cron_job", owningCronJob.Name)
			// add breadcrumb with cronJob timestamps
			scope.AddBreadcrumb(&sentry.Breadcrumb{
				Message:   fmt.Sprintf("Cronjob %s created", owningCronJob.Name),
				Level:     sentry.LevelInfo,
				Timestamp: owningCronJob.CreationTimestamp.Time,
			}, breadcrumbLimit)
			metadataJson, err := prettyJson(owningCronJob.ObjectMeta)
			if err == nil {
				scope.SetExtra("cronJob Metadata", metadataJson)
			}
			// the job is not owned by a cronJob
		} else {
			owner := pod.OwnerReferences[0]
			sentryEvent.Fingerprint = append(sentryEvent.Fingerprint, owner.Kind, owner.Name)
		}
	}

	logger.Trace().Msgf("Fingerprint after adjustment: %v", sentryEvent.Fingerprint)

	addPodLogLinkToGKEContext(ctx, scope, podName, namespace)

	return nil
}
