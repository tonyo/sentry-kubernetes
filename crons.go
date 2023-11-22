package main

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/getsentry/sentry-go"
	batchv1 "k8s.io/api/batch/v1"
	v1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/informers"
	"k8s.io/client-go/tools/cache"
)

func startCronsInformers(ctx context.Context, namespace string) error {

	clientset, err := getClientsetFromContext(ctx)
	if err != nil {
		return errors.New("failed to get clientset")
	}

	factory := informers.NewSharedInformerFactoryWithOptions(
		clientset,
		5*time.Second,
		informers.WithNamespace(namespace),
	)

	cronjobInformer, err := createCronjobInformer(ctx, factory, namespace)
	if err != nil {
		return err
	}
	jobInformer, err := createJobInformer(ctx, factory, namespace)
	if err != nil {
		return err
	}

	doneChan := make(chan struct{})
	factory.Start(doneChan)

	if ok := cache.WaitForCacheSync(doneChan, cronjobInformer.HasSynced); !ok {
		return errors.New("cronjob informer failed to sync")
	}

	if ok := cache.WaitForCacheSync(doneChan, jobInformer.HasSynced); !ok {
		return errors.New("job informer failed to sync")
	}

	<-doneChan

	return nil
}

func runSentryCronsCheckin(ctx context.Context, job *batchv1.Job, checkinAction string) error {

	val := ctx.Value(CronsInformerDataKey{})
	if val == nil {
		return errors.New("no crons informer data struct given")
	}
	var cronsInformerData *map[string]CronsMonitorData
	var ok bool
	if cronsInformerData, ok = val.(*map[string]CronsMonitorData); !ok {
		return errors.New("cannot convert cronsInformerData value from context")
	}

	clientset, err := getClientsetFromContext(ctx)
	if err != nil {
		return err
	}

	if len(job.OwnerReferences) == 0 {
		return errors.New("job does not have cronjob reference")
	}

	jobRef := job.OwnerReferences[0]
	if !*jobRef.Controller || jobRef.Kind != "CronJob" {
		return errors.New("job does not have cronjob reference")
	}

	owningCronJob, err := clientset.BatchV1().CronJobs(job.Namespace).Get(context.Background(), jobRef.Name, metav1.GetOptions{})
	if err != nil {
		return err
	}

	cronJobData, ok := (*cronsInformerData)[owningCronJob.Name]
	if !ok {
		return errors.New("cannot find cronJob data")
	}

	// Add the job to the cronJob informer data
	// since there are at least one active pod
	if checkinAction == "add" {

		// check if job already added to jobData slice
		_, ok := cronJobData.JobDatas[job.Name]
		if ok {
			return nil
		}
		fmt.Printf("checking in at start of job: %s\n", job.Name)
		// all containers running in the pod
		checkinId := sentry.CaptureCheckIn(
			&sentry.CheckIn{
				MonitorSlug: cronJobData.MonitorSlug,
				Status:      sentry.CheckInStatusInProgress,
			},
			cronJobData.monitorConfig,
		)
		cronJobData.addJob(job, *checkinId)

		// Delete pod from the cronJob informer data
	} else if checkinAction == "update" || checkinAction == "delete" {

		// do not check in to exit if there are still active pods
		if job.Status.Active > 0 {
			return nil
		}

		// get required number of pods to complete
		var requiredCompletions int32
		if owningCronJob.Spec.JobTemplate.Spec.Completions == nil {
			// if not set, any pod success is enough
			requiredCompletions = 1
		} else {
			requiredCompletions = *owningCronJob.Spec.JobTemplate.Spec.Completions
		}

		// Check desired number of pods have succeeded
		var jobStatus sentry.CheckInStatus
		if job.Status.Succeeded >= requiredCompletions {
			jobStatus = sentry.CheckInStatusOK
		} else {
			jobStatus = sentry.CheckInStatusError
		}

		// Get job data to retrieve the checkin ID
		jobData, ok := cronJobData.JobDatas[job.Name]
		if !ok {
			return nil
		}

		fmt.Printf("checking in at end of job: %s\n", job.Name)
		sentry.CaptureCheckIn(
			&sentry.CheckIn{
				ID:          jobData.getCheckinId(),
				MonitorSlug: cronJobData.MonitorSlug,
				Status:      jobStatus,
			},
			cronJobData.monitorConfig,
		)
	}

	return nil
}

func runCronsDataHandler(ctx context.Context, scope *sentry.Scope, pod *v1.Pod, sentryEvent *sentry.Event) error {

	// get owningCronJob if exists
	owningCronJob, err := getOwningCronJob(ctx, pod)
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

func getOwningCronJob(ctx context.Context, pod *v1.Pod) (*batchv1.CronJob, error) {

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
