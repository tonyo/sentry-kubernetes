package main

import (
	"context"
	"fmt"
	"os"
	"strings"
	"time"

	"github.com/getsentry/sentry-go"
	"github.com/rs/zerolog"
	globalLogger "github.com/rs/zerolog/log"
	v1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/util/validation"
	k8sVersion "k8s.io/apimachinery/pkg/version"
	"k8s.io/apimachinery/pkg/watch"
	"k8s.io/client-go/discovery"
	"k8s.io/client-go/kubernetes"
	"k8s.io/client-go/rest"
	"k8s.io/client-go/tools/cache"
	toolsWatch "k8s.io/client-go/tools/watch"
)

func getObjectNameTag(object *v1.ObjectReference) string {
	if object.Kind == "" {
		return "object_name"
	} else {
		return fmt.Sprintf("%s_name", strings.ToLower(object.Kind))
	}
}

func processKubernetesEvent(ctx context.Context, eventObject *v1.Event) {
	logger := zerolog.Ctx(ctx)

	logger.Debug().Msgf("EventObject: %#v", eventObject)
	logger.Debug().Msgf("Event type: %#v", eventObject.Type)

	originalEvent := eventObject.DeepCopy()
	eventObject = eventObject.DeepCopy()

	involvedObject := eventObject.InvolvedObject

	hub := sentry.GetHubFromContext(ctx)
	if hub == nil {
		logger.Error().Msgf("Cannot get Sentry hub from context")
		return
	}

	hub.WithScope(func(scope *sentry.Scope) {
		scope.SetTag("event_type", eventObject.Type)
		scope.SetTag("reason", eventObject.Reason)
		scope.SetTag("namespace", involvedObject.Namespace)
		scope.SetTag("kind", involvedObject.Kind)
		scope.SetTag("object_uid", string(involvedObject.UID))

		name_tag := getObjectNameTag(&involvedObject)
		scope.SetTag(name_tag, involvedObject.Name)

		if source, err := prettyJson(eventObject.Source); err == nil {
			scope.SetExtra("Event Source", source)
		}
		eventObject.Source = v1.EventSource{}

		if involvedObject, err := prettyJson(eventObject.InvolvedObject); err == nil {
			scope.SetExtra("Involved Object", involvedObject)
		}
		eventObject.InvolvedObject = v1.ObjectReference{}

		// clean-up the event a bit
		eventObject.ObjectMeta.ManagedFields = []metav1.ManagedFieldsEntry{}
		if metadata, err := prettyJson(eventObject.ObjectMeta); err == nil {
			scope.SetExtra("Event Metadata", metadata)
		}
		eventObject.ObjectMeta = metav1.ObjectMeta{}

		// The entire event
		if kubeEvent, err := prettyJson(eventObject); err == nil {
			scope.SetExtra("~ Misc Event Fields", kubeEvent)
		}

		runEnhancers(ctx, originalEvent, scope)

		sentryEvent := &sentry.Event{Message: eventObject.Message, Level: sentry.LevelError}
		hub.CaptureEvent(sentryEvent)
	})

}

func handleWatchEvent(ctx context.Context, event *watch.Event, cutoffTime metav1.Time) {
	logger := zerolog.Ctx(ctx)

	eventObjectRaw := event.Object
	// Watch event type: Added, Delete, Bookmark...
	if (event.Type != watch.Added) && (event.Type != watch.Modified) {
		logger.Debug().Msgf("Skipping a watch event of type %s", event.Type)
		return
	}

	objectKind := eventObjectRaw.GetObjectKind()
	eventObject, ok := eventObjectRaw.(*v1.Event)
	if !ok {
		logger.Warn().Msgf("Skipping an event of kind '%v' because it cannot be casted", objectKind)
		return
	}

	// Get event timestamp
	eventTs := eventObject.LastTimestamp
	if eventTs.IsZero() {
		eventTs = metav1.Time(eventObject.EventTime)
	}

	if !cutoffTime.IsZero() && !eventTs.IsZero() && eventTs.Before(&cutoffTime) {
		logger.Debug().Msgf("Ignoring an event because it is too old")
		return
	}

	if eventObject.Type == v1.EventTypeNormal {
		logger.Debug().Msgf("Skipping an event of type %s", eventObject.Type)
		return
	}

	processKubernetesEvent(ctx, eventObject)

	// FIXME: we should also Normal events to the buffer
	addEventToBuffer(eventObject)
}

func watchEventsInNamespace(ctx context.Context, namespace string, watchSince time.Time) (err error) {
	logger := zerolog.Ctx(ctx)

	clientset, err := getClientsetFromContext(ctx)
	if err != nil {
		return err
	}

	watchFunc := func(options metav1.ListOptions) (watch.Interface, error) {
		opts := metav1.ListOptions{
			Watch: true,
		}
		return clientset.CoreV1().Events(namespace).Watch(ctx, opts)
	}
	logger.Debug().Msg("Getting the event watcher...")
	retryWatcher, err := toolsWatch.NewRetryWatcher("1", &cache.ListWatch{WatchFunc: watchFunc})
	if err != nil {
		return err
	}

	watchCh := retryWatcher.ResultChan()
	defer retryWatcher.Stop()

	watchSinceWrapped := metav1.Time{Time: watchSince}

	logger.Debug().Msg("Reading from the event channel...")
	for event := range watchCh {
		handleWatchEvent(ctx, &event, watchSinceWrapped)
	}

	return nil
}

func watchEventsInNamespaceForever(ctx context.Context, config *rest.Config, namespace string) error {
	localHub := sentry.CurrentHub().Clone()
	ctx = sentry.SetHubOnContext(ctx, localHub)

	// Attach the "namespace" tag to logger
	logger := (zerolog.Ctx(ctx).With().
		Str("namespace", namespace).
		Logger())
	ctx = logger.WithContext(ctx)

	where := fmt.Sprintf("in namespace '%s'", namespace)
	if namespace == v1.NamespaceAll {
		where = "in all namespaces"
	}

	watchFromBeginning := isTruthy(os.Getenv("SENTRY_K8S_WATCH_HISTORICAL"))
	var watchSince time.Time
	if watchFromBeginning {
		watchSince = time.Time{}
		logger.Info().Msgf("Watching all available events (no starting timestamp)")
	} else {
		watchSince = time.Now()
		logger.Info().Msgf("Watching events starting from: %s", watchSince.Format("Mon, 02 Jan 2006 15:04:05 -0700"))
	}

	clientset, err := kubernetes.NewForConfig(config)
	if err != nil {
		return err
	}

	ctx = setClientsetOnContext(ctx, clientset)

	for {
		if err := watchEventsInNamespace(ctx, namespace, watchSince); err != nil {
			logger.Error().Msgf("Error while watching events %s: %s", where, err)
		}
		watchSince = time.Now()
		time.Sleep(time.Second * 1)
	}
}

func getClusterVersion(config *rest.Config) (*k8sVersion.Info, error) {
	versionInfo := &k8sVersion.Info{}

	discoveryClient, err := discovery.NewDiscoveryClientForConfig(config)
	if err != nil {
		return versionInfo, err
	}
	globalLogger.Debug().Msgf("Fetching cluster version...")
	versionInfo, err = discoveryClient.ServerVersion()
	globalLogger.Debug().Msgf("Cluster version: %s", versionInfo)
	return versionInfo, err
}

func setKubernetesSentryContext(config *rest.Config) {
	kubernetesContext := map[string]interface{}{
		"API endpoint": config.Host,
	}

	// Get cluster version via API
	clusterVersion, err := getClusterVersion(config)
	if err == nil {
		kubernetesContext["Server version"] = clusterVersion.String()
	} else {
		globalLogger.Error().Msgf("Error while getting cluster version: %s", err)
	}

	sentry.CurrentHub().Scope().SetContext(
		"Kubernetes",
		kubernetesContext,
	)
}

var defaultNamespacesToWatch = []string{v1.NamespaceDefault}

const allNamespacesLabel = "__all__"

func getNamespacesToWatch() (watchAll bool, namespaces []string, err error) {
	watchNamespacesRaw := strings.TrimSpace(os.Getenv("SENTRY_K8S_WATCH_NAMESPACES"))

	// Nothing in the env variable => use the default value
	if watchNamespacesRaw == "" {
		return false, defaultNamespacesToWatch, nil
	}

	// Special label => watch all namespaces
	if watchNamespacesRaw == allNamespacesLabel {
		return true, []string{}, nil
	}

	rawNamespaces := strings.Split(watchNamespacesRaw, ",")
	namespaces = make([]string, 0, len(rawNamespaces))
	for _, rawNamespace := range rawNamespaces {
		namespace := strings.TrimSpace(rawNamespace)
		if namespace == "" {
			continue
		}
		errors := validation.IsValidLabelValue(namespace)
		if len(errors) != 0 {
			// Not a valid namespace name
			return false, []string{}, fmt.Errorf(errors[0])
		}
		namespaces = append(namespaces, namespace)
	}
	namespaces = removeDuplicates(namespaces)
	if len(namespaces) == 0 {
		return false, namespaces, fmt.Errorf("no namespaces specified")
	}

	return false, namespaces, nil
}

func configureLogging() {
	globalLogger.Logger = globalLogger.Output(zerolog.ConsoleWriter{Out: os.Stdout})
}

func setGlobalSentryTags() {
	for _, e := range os.Environ() {
		pair := strings.SplitN(e, "=", 2)
		if len(pair) != 2 {
			continue
		}
		key, value := strings.TrimSpace(pair[0]), strings.TrimSpace(pair[1])
		tagPrefix := "SENTRY_K8S_GLOBAL_TAG_"
		if strings.HasPrefix(key, tagPrefix) {
			tagKey := strings.TrimPrefix(key, tagPrefix)
			globalLogger.Info().Msgf("Global tag detected: %s=%s", tagKey, value)
			sentry.CurrentHub().Scope().SetTag(tagKey, value)
		}
	}
}

func main() {
	configureLogging()
	initSentrySDK()
	defer sentry.Flush(time.Second)

	config, err := getClusterConfig()
	if err != nil {
		globalLogger.Fatal().Msgf("Config init error: %s", err)
	}

	setKubernetesSentryContext(config)
	setGlobalSentryTags()

	watchAllNamespaces, namespaces, err := getNamespacesToWatch()
	if err != nil {
		globalLogger.Fatal().Msgf("Cannot parse namespaces to watch: %s", err)
	}

	if watchAllNamespaces {
		namespaces = []string{v1.NamespaceAll}
	}

	// klog.InitFlags(nil)
	// flag.Set("v", "8")
	// flag.Parse()

	ctx := globalLogger.Logger.WithContext(context.Background())
	for _, namespace := range namespaces {
		go watchEventsInNamespaceForever(ctx, config, namespace)
	}

	// Sleep forever
	select {}
}
