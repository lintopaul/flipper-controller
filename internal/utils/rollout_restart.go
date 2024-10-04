package utils

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/lintopaul/flipper-controller/internal/constants"

	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
	"k8s.io/client-go/tools/record"
	"sigs.k8s.io/controller-runtime/pkg/client"
	ctrlclient "sigs.k8s.io/controller-runtime/pkg/client"
)

// HandleRolloutRestart handles rollout restart of object by patching with annotation
func HandleRolloutRestart(ctx context.Context, client ctrlclient.Client,
	obj ctrlclient.Object, managedByValue string, restartTimeInRFC3339 string) error {
	// log := log.FromContext(ctx)

	switch t := obj.(type) {
	case *appsv1.Deployment:
		patch := ctrlclient.StrategicMergeFrom(t.DeepCopy())
		if t.Annotations == nil {
			t.Annotations = make(map[string]string)
		}
		t.Annotations[constants.RolloutManagedBy] = managedByValue
		if restartTimeInRFC3339 == "" {
			restartTimeInRFC3339 = time.Now().Format(time.RFC3339)
		}

		t.Annotations[constants.AnnotationFlipperRestartedAt] = restartTimeInRFC3339
		// t.Spec.Template.ObjectMeta.Annotations[constants.RolloutRestartAnnotation] = restartTimeInRFC3339

		// TODO wait for pods to be ready before proceeding and followed by annotation completedAt:time?
		return client.Patch(ctx, t, patch)
	default:
		return fmt.Errorf(constants.ErrorUnsupportedKind, t)
	}
}

// HandleRolloutRestartList handles rollout restart for list of objects
// Currently support is only appsv1.DeploymentList
func HandleRolloutRestartList(ctx context.Context, k8sclient ctrlclient.Client, Objectlist client.ObjectList,
	recorder record.EventRecorder, flipperNamespacedName string,
	restartTimeInRFC3339 string) (failedObjList client.ObjectList, errs error) {
	var err error
	switch t := Objectlist.(type) {
	case *appsv1.DeploymentList:
		failedRolloutDeploymentList := &appsv1.DeploymentList{}
		if t != nil && len(t.Items) > 0 {
			for _, obj := range t.Items {
				objCopied := obj.DeepCopy()
				if objCopied.Annotations == nil {
					objCopied.Annotations = make(map[string]string)
				}

				if err = HandleRolloutRestart(ctx, k8sclient, objCopied,
					flipperNamespacedName, restartTimeInRFC3339); err != nil {
					if client.IgnoreNotFound(err) != nil {
						errs = errors.Join(errs, err)
						failedRolloutDeploymentList.Items = append(failedRolloutDeploymentList.Items, obj)
						recorder.Eventf(objCopied, corev1.EventTypeWarning, constants.ReasonRolloutRestartFailed,
							"Rollout restart failed for target %#v: err=%s", objCopied, err)
					} else {
						recorder.Eventf(objCopied, corev1.EventTypeWarning, constants.ReasonRolloutRestartFailed,
							"Listed Object not found (mightbeDeleted) %#v: err=%s", objCopied, err)
					}
				} else {
					recorder.Eventf(objCopied, corev1.EventTypeNormal, constants.ReasonRolloutRestartTriggered,
						"Rollout restart triggered for %v", objCopied)
				}
			}
		}
		failedObjList = failedRolloutDeploymentList
	default:
		errs = fmt.Errorf(constants.ErrorUnsupportedKind, t)
	}

	return
}
