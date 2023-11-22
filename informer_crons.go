package main

import (
	"context"
	"errors"
	"fmt"

	batchv1 "k8s.io/api/batch/v1"

	"k8s.io/client-go/informers"
	"k8s.io/client-go/tools/cache"
)

func createCronjobInformer(ctx context.Context, factory informers.SharedInformerFactory, namespace string) (cache.SharedIndexInformer, error) {
	fmt.Printf("starting cronJob informer\n")

	val := ctx.Value(CronsInformerDataKey{})
	if val == nil {
		return nil, errors.New("no crons informer data struct given")
	}

	var cronsInformerData *map[string]CronsMonitorData
	var ok bool
	if cronsInformerData, ok = val.(*map[string]CronsMonitorData); !ok {
		return nil, errors.New("cannot convert cronsInformerData value from context")
	}

	done := make(chan struct{})
	defer close(done)

	cronjobInformer := factory.Batch().V1().CronJobs().Informer()

	var handler cache.ResourceEventHandlerFuncs

	handler.AddFunc = func(obj interface{}) {
		cronjob := obj.(*batchv1.CronJob)
		fmt.Printf("ADD: CronJob Added to Store: %s\n", cronjob.GetName())
		_, ok := (*cronsInformerData)[cronjob.Name]
		if ok {
			fmt.Printf("cronJob %s already exists in the crons informer data struct...\n", cronjob.Name)
		} else {
			(*cronsInformerData)[cronjob.Name] = *NewCronsMonitorData(cronjob.Name, cronjob.Spec.Schedule, 5, 3)
		}
	}

	// handler.UpdateFunc = func(oldObj, newObj interface{}) {

	// 	oldCronjob := oldObj.(*batchv1.CronJob)
	// 	newCronjob := newObj.(*batchv1.CronJob)

	// 	if oldCronjob.ResourceVersion == newCronjob.ResourceVersion {
	// 		fmt.Printf("Informer event: Event sync %s/%s\n", oldCronjob.GetNamespace(), oldCronjob.GetName())
	// 	}

	// 	for _, objRef := range newCronjob.Status.Active {
	// 		runSentryCronsCheckin(ctx, objRef)
	// 	}

	// }

	handler.DeleteFunc = func(obj interface{}) {
		cronjob := obj.(*batchv1.CronJob)
		// fmt.Printf("Informer event: Event DELETED %s/%s\n", cm.GetNamespace(), cm.GetName())
		fmt.Printf("DELETE: CronJob deleted from Store: %s\n", cronjob.GetName())
		_, ok := (*cronsInformerData)[cronjob.Name]
		if ok {
			delete((*cronsInformerData), cronjob.Name)
			fmt.Printf("cronJob %s deleted from the crons informer data struct...\n", cronjob.Name)
		} else {
			fmt.Printf("cronJob %s not in the crons informer data struct...\n", cronjob.Name)
		}
	}

	cronjobInformer.AddEventHandler(handler)

	return cronjobInformer, nil
}
