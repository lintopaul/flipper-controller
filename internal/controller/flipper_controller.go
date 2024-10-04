/*
Copyright 2024.

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

package controller

import (
	"context"
	"time"

	appsv1alpha1 "github.com/lintopaul/flipper-controller/api/v1alpha1"
	"github.com/lintopaul/flipper-controller/internal/constants"
	"github.com/lintopaul/flipper-controller/internal/utils"
	appsv1 "k8s.io/api/apps/v1"
	v1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/labels"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/client-go/tools/record"

	"k8s.io/client-go/util/workqueue"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/controller"
	"sigs.k8s.io/controller-runtime/pkg/log"
	"sigs.k8s.io/controller-runtime/pkg/reconcile"
)

// FlipperReconciler reconciles a Flipper object
type FlipperReconciler struct {
	client.Client
	Scheme   *runtime.Scheme
	Recorder record.EventRecorder
}

// +kubebuilder:rbac:groups=apps.flipper.io,resources=flippers,verbs=get;list;watch;create;update;patch;delete
// +kubebuilder:rbac:groups=apps.flipper.io,resources=flippers/status,verbs=get;update;patch
// +kubebuilder:rbac:groups=apps.flipper.io,resources=flippers/finalizers,verbs=update
// +kubebuilder:rbac:groups=*,resources=deployments,verbs=get;list;watch;create;update;patch
// +kubebuilder:rbac:groups=*,resources=pods,verbs=get;list;watch;create;update;patch
// +kubebuilder:rbac:groups=*,resources=events,verbs=get;list;watch;create;update;patch

// Reconcile handles the reconciliation loop for the Flipper CRD.
// The function ensures the current state of the cluster matches the desired state described in the Flipper resource.
func (r *FlipperReconciler) Reconcile(ctx context.Context, req ctrl.Request) (ctrl.Result, error) {
	log := log.FromContext(ctx)
	var err error
	// Fetch the Flipper object based on the namespaced name from the request
	flipper := &appsv1alpha1.Flipper{}
	log.Info("Reconciling flipper %s", "NamespacedName", req.NamespacedName)
	if err = r.Get(ctx, req.NamespacedName, flipper); err != nil {
		// If the Flipper object is not found, we ignore the error because it may have been deleted
		log.Error(err, "Unable to get the flipper object")
		return ctrl.Result{}, client.IgnoreNotFound(err)
	}

	log.Info("flipper Spec", "flipperSpec", flipper)

	// Handle the rollout reconciliation logic
	return r.HandleRolloutReconciler(ctx, flipper)

}

// HandleRolloutReconciler performs the rollout restart based on the Flipper object
func (r *FlipperReconciler) HandleRolloutReconciler(ctx context.Context, flipper *appsv1alpha1.Flipper) (ctrl.Result, error) {
	log := log.FromContext(ctx)

	var (
		rolloutNameSpace    = flipper.Spec.Match.Namespace
		rolloutInterval, _  = time.ParseDuration(flipper.Spec.Interval)
		deploymentList      = &appsv1.DeploymentList{}
		readyDeploymentList = &appsv1.DeploymentList{
			Items: []appsv1.Deployment{},
		}
		labelSelector = labels.Set{"mesh": "true"}.AsSelector()
		listOptions   = &client.ListOptions{
			Namespace:     rolloutNameSpace,
			LabelSelector: labelSelector,
		}
		rolloutTime  = time.Now()
		nowInRFC3339 = rolloutTime.Format(time.RFC3339)
	)
	if rolloutInterval == 0 {
		rolloutInterval = constants.FlipperInterval
	}

	log.Info("Checking Phase of Flipper CR ", "Phase", flipper.Status.Phase)

	// Switch based on the current phase of the Flipper CR
	switch flipper.Status.Phase {
	case "", appsv1alpha1.FlipPending:
		// List all deployments based on the list options (label and namespace)
		if err := r.Client.List(ctx, deploymentList, listOptions); client.IgnoreNotFound(err) != nil {
			log.Error(err, "Error in listing the deployments")
			flipper.Status.Reason = "Error in listing the deployments"
			r.Recorder.Event(flipper, v1.EventTypeWarning,
				"RolloutRestartFailed", "Unable to list Deployments")
			flipper.Status.Phase = appsv1alpha1.FlipFailed
			if err := r.Status().Update(ctx, flipper); err != nil {
				r.Recorder.Event(flipper, v1.EventTypeWarning,
					"RolloutRestartFailed", "Unable to update flipperStatus")
				return ctrl.Result{}, err
			}
			return ctrl.Result{}, err
		}

		// Filter and collect only the deployments that are fully ready (replicas match)
		readyDeploymentList = &appsv1.DeploymentList{
			Items: []appsv1.Deployment{},
		}

		for _, deployment := range deploymentList.Items {
			if deployment.Status.ReadyReplicas == *deployment.Spec.Replicas {
				readyDeploymentList.Items = append(readyDeploymentList.Items, deployment)
			} else {
				// Log and record events for any deployment not ready
				r.Recorder.Eventf(flipper, v1.EventTypeWarning,
					"RolloutRestartWarning", "Deployment %s/%s not ready ignoring for rollout",
					deployment.GetNamespace(), deployment.GetName())
			}
		}

		// If no ready deployments, move back to pending phase
		if len(readyDeploymentList.Items) == 0 {
			flipper.Status.Phase = appsv1alpha1.FlipPending
			if err := r.Status().Update(ctx, flipper); err != nil {
				r.Recorder.Event(flipper, v1.EventTypeWarning,
					"RolloutRestartFailed", "Unable to update flipperStatus")
				return ctrl.Result{}, err
			}
			r.Recorder.Event(flipper, v1.EventTypeWarning,
				"RolloutRestartPending", "No objects found")
			return ctrl.Result{RequeueAfter: constants.RequeueInterval}, nil
		}
	case appsv1alpha1.FlipFailed:
		// Handle the case where the rollout failed previously, retrying for failed deployments
		for _, failedDeploymentInfo := range flipper.Status.FailedRolloutDeployments {
			failedDeployment := appsv1.Deployment{}
			// Attempt to get the failed deployment from the cluster
			if err := r.Client.Get(ctx, types.NamespacedName{
				Namespace: failedDeploymentInfo.Namespace,
				Name:      failedDeploymentInfo.Name,
			}, &failedDeployment); err == nil {
				// If the deployment is now ready, add to the list for rollout
				if failedDeployment.Status.ReadyReplicas == *failedDeployment.Spec.Replicas {
					readyDeploymentList.Items = append(readyDeploymentList.Items, failedDeployment)
				} else {
					// Record an event for any deployment still not ready
					r.Recorder.Eventf(flipper, v1.EventTypeWarning,
						"RolloutRestartWarning", "Deployment %s/%s not ready ignoring for rollout",
						failedDeployment.GetNamespace(), failedDeployment.GetName())
				}
			}
		}
	case appsv1alpha1.FlipSucceeded:
		// Handle successful rollouts and ensure that we follow the specified interval
		if flipper.Status.LastScheduledRolloutTime.Add(rolloutInterval).Compare(time.Now()) <= 0 {
			// If enough time has passed, trigger another rollout
			flipper.Status.Phase = appsv1alpha1.FlipPending
			r.Recorder.Event(flipper, v1.EventTypeNormal,
				"RolloutRestartInit", "Triggering next scheduled")
			r.Recorder.Event(flipper, v1.EventTypeNormal,
				"RolloutRestartInit", "Moving state to pending")
			if err := r.Status().Update(ctx, flipper); err != nil {
				r.Recorder.Event(flipper, v1.EventTypeWarning,
					"RolloutRestartFailed", "Unable to update flipperStatus")
				return ctrl.Result{}, err
			}
			return ctrl.Result{Requeue: true}, nil
		} else {
			// If the interval has not passed, requeue for the next rollout check
			return ctrl.Result{RequeueAfter: time.Until(flipper.Status.LastScheduledRolloutTime.Add(rolloutInterval))}, nil
		}
	}

	// Reset failed deployments list
	flipper.Status.FailedRolloutDeployments = []appsv1alpha1.DeploymentInfo{}

	// Handle the rollout restart by invoking the helper function to restart deployments
	if failedObjList, err := utils.HandleRolloutRestartList(ctx, r.Client, deploymentList,
		r.Recorder, flipper.Namespace+"/"+flipper.Name, nowInRFC3339); err != nil {
		// Handle any errors during the restart
		r.Recorder.Eventf(flipper, v1.EventTypeWarning,
			"RolloutRestartFailed", "Error", err.Error())
		flipper.Status.Phase = appsv1alpha1.FlipFailed
		// Log any failed deployments for rollback
		if failedDeploymentList, ok := failedObjList.(*appsv1.DeploymentList); ok && failedDeploymentList != nil {
			for _, failedDeployment := range failedDeploymentList.Items {
				flipper.Status.FailedRolloutDeployments = append(flipper.Status.FailedRolloutDeployments,
					appsv1alpha1.DeploymentInfo{Name: failedDeployment.Name, Namespace: failedDeployment.Namespace})
			}
		}
		// Update Flipper status with the failed deployments
		errUpdate := r.Status().Update(ctx, flipper)
		if errUpdate != nil {
			return ctrl.Result{}, errUpdate
		}
		return ctrl.Result{}, err
	} else {
		// If the restart succeeds, log the success and update the Flipper status
		r.Recorder.Eventf(flipper, v1.EventTypeNormal,
			"RolloutRestartSucceeded", "flipper %s succeeded", flipper.Name)
		flipper.Status.Phase = appsv1alpha1.FlipSucceeded
		flipper.Status.LastScheduledRolloutTime = metav1.NewTime(rolloutTime)
		err := r.Status().Update(ctx, flipper)
		if err != nil {
			return ctrl.Result{}, err
		}
	}

	// Requeue after the defined rollout interval
	return ctrl.Result{RequeueAfter: rolloutInterval}, nil
}

// SetupWithManager sets up the controller with the Manager.
func (r *FlipperReconciler) SetupWithManager(mgr ctrl.Manager) error {
	return ctrl.NewControllerManagedBy(mgr).
		WithOptions(controller.Options{
			MaxConcurrentReconciles: 5,
			RateLimiter: workqueue.NewTypedItemExponentialFailureRateLimiter[reconcile.Request](
				5*time.Second,   // Initial backoff
				300*time.Second, // Maximum backoff
			),
		}).
		For(&appsv1alpha1.Flipper{}).
		Complete(r)
}
