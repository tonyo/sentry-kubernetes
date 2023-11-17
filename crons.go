package main

import (
	"context"
	"errors"
	"fmt"

	"github.com/getsentry/sentry-go"
	batchv1 "k8s.io/api/batch/v1"
	v1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
)

func runCronsDataHandler(ctx context.Context, scope *sentry.Scope, pod *v1.Pod, sentryEvent *sentry.Event) error {

	// get owningCronJob if exists
	owningCronJob, err := getOwningCronJob(ctx, pod, scope)
	if err != nil {
		return err
	}
	if owningCronJob == nil {
		return errors.New("cronjob: pod not created under a cronjob")
	}

	scope.SetContext("Monitor", sentry.Context{
		"Slug": owningCronJob.Name,
	})

	sentryEvent.Fingerprint = append(sentryEvent.Fingerprint, owningCronJob.Kind, owningCronJob.Name)

	setTagIfNotEmpty(scope, "cronjob_name", owningCronJob.Name)

	// add breadcrumb with cronJob timestamps
	scope.AddBreadcrumb(&sentry.Breadcrumb{
		Message:   fmt.Sprintf("Created cronjob %s", owningCronJob.Name),
		Level:     sentry.LevelInfo,
		Timestamp: owningCronJob.CreationTimestamp.Time,
	}, breadcrumbLimit)

	metadataJson, err := prettyJson(owningCronJob.ObjectMeta)

	if err == nil {
		scope.SetContext("Cronjob", sentry.Context{
			"Metadata": metadataJson,
		})
	} else {
		return err
	}
	return nil
}

func getOwningCronJob(ctx context.Context, pod *v1.Pod, scope *sentry.Scope) (*batchv1.CronJob, error) {

	clientset, err := getClientsetFromContext(ctx)
	if err != nil {
		return nil, err
	}

	namespace := pod.Namespace

	// first attempt to group events by cronJobs
	var owningCronJob *batchv1.CronJob = nil

	// check if the pod corresponds to a cronJob
	for _, podRef := range pod.ObjectMeta.OwnerReferences {
		// check the pod has a job as an owner
		if !*podRef.Controller || podRef.Kind != "Job" {
			continue
		}
		// find the owning job
		owningJob, err := clientset.BatchV1().Jobs(namespace).Get(context.Background(), podRef.Name, metav1.GetOptions{})
		if err != nil {
			continue
		}
		// check if owning job is owned by a cronJob
		for _, jobRef := range owningJob.ObjectMeta.OwnerReferences {
			if !*jobRef.Controller || jobRef.Kind != "CronJob" {
				continue
			}
			owningCronJob, err = clientset.BatchV1().CronJobs(namespace).Get(context.Background(), jobRef.Name, metav1.GetOptions{})
			if err != nil {
				continue
			}
		}
	}

	return owningCronJob, nil
}
