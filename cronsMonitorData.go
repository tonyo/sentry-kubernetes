package main

import (
	"github.com/getsentry/sentry-go"
	batchv1 "k8s.io/api/batch/v1"
)

type CronsInformerDataKey struct{}

// struct associated with a job
type CronsJobData struct {
	CheckinId sentry.EventID
}

// constructor for cronsMonitorData
func NewCronsJobData(checkinId sentry.EventID) *CronsJobData {
	return &CronsJobData{
		CheckinId: checkinId,
	}
}

func (j *CronsJobData) getCheckinId() sentry.EventID {
	return j.CheckinId
}

// struct associated with a cronJob
type CronsMonitorData struct {
	MonitorSlug   string
	monitorConfig *sentry.MonitorConfig
	JobDatas      map[string]*CronsJobData
}

// constructor for cronsMonitorData
func NewCronsMonitorData(monitorSlug string, schedule string, maxRunTime int64, checkinMargin int64) *CronsMonitorData {

	monitorSchedule := sentry.CrontabSchedule(schedule)
	return &CronsMonitorData{
		MonitorSlug: monitorSlug,
		monitorConfig: &sentry.MonitorConfig{
			Schedule:      monitorSchedule,
			MaxRuntime:    maxRunTime,
			CheckInMargin: checkinMargin,
		},
		JobDatas: make(map[string]*CronsJobData),
	}
}

// add a job to the crons monitor
func (c *CronsMonitorData) addJob(job *batchv1.Job, checkinId sentry.EventID) error {
	c.JobDatas[job.Name] = NewCronsJobData(checkinId)
	return nil
}

func (c *CronsMonitorData) String() string {
	return "MonitorSlug: " + c.MonitorSlug
}
