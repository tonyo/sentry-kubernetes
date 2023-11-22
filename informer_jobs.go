package main

import (
	"context"
	"errors"
	"fmt"

	batchv1 "k8s.io/api/batch/v1"

	"k8s.io/client-go/informers"
	"k8s.io/client-go/tools/cache"
)

func createJobInformer(ctx context.Context, factory informers.SharedInformerFactory, namespace string) (cache.SharedIndexInformer, error) {
	fmt.Printf("starting job informer\n")

	val := ctx.Value(CronsInformerDataKey{})
	if val == nil {
		return nil, errors.New("no crons informer data struct given")
	}

	done := make(chan struct{})
	defer close(done)

	jobInformer := factory.Batch().V1().Jobs().Informer()

	var handler cache.ResourceEventHandlerFuncs

	handler.AddFunc = func(obj interface{}) {
		job := obj.(*batchv1.Job)
		fmt.Printf("ADD: Job Added to Store: %s\n", job.GetName())
		err := runSentryCronsCheckin(ctx, job, "add")
		if err != nil {
			fmt.Println(err)
		}
	}

	handler.UpdateFunc = func(oldObj, newObj interface{}) {

		oldJob := oldObj.(*batchv1.Job)
		newJob := newObj.(*batchv1.Job)

		if oldJob.ResourceVersion == newJob.ResourceVersion {
			fmt.Printf("Informer event: Event sync %s/%s\n", oldJob.GetNamespace(), oldJob.GetName())
		} else {
			runSentryCronsCheckin(ctx, newJob, "update")
		}
	}

	handler.DeleteFunc = func(obj interface{}) {
		job := obj.(*batchv1.Job)
		// fmt.Printf("Informer event: Event DELETED %s/%s\n", cm.GetNamespace(), cm.GetName())
		fmt.Printf("DELETE: Job deleted from Store: %s\n", job.GetName())
		err := runSentryCronsCheckin(ctx, job, "delete")
		if err != nil {
			fmt.Println(err)
		}
	}

	jobInformer.AddEventHandler(handler)

	return jobInformer, nil
}
