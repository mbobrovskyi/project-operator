/*
Copyright 2024 Mykhailo Bobrovskyi.

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
	goerrors "errors"
	"fmt"
	opsv1alpha1 "github.com/mbobrovskyi/project-operator/api/v1alpha1"
	v1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/api/meta"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/types"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/controller/controllerutil"
	"sigs.k8s.io/controller-runtime/pkg/log"
)

const ProjectFinalizerName = "ops.local/project"

// Definitions to manage status conditions
const (
	// typeAvailableProject represents the status of the Project reconciliation
	typeAvailableProject = "Available"
	// typeDegradedProject represents the status used when the custom resource is deleted and the finalizer operations are yet to occur.
	typeDegradedProject = "Degraded"
)

// ProjectReconciler reconciles a Project object
type ProjectReconciler struct {
	client.Client
	Scheme *runtime.Scheme
}

//+kubebuilder:rbac:groups=ops.local,resources=projects,verbs=get;list;watch;create;update;patch;delete
//+kubebuilder:rbac:groups=ops.local,resources=projects/status,verbs=get;update;patch
//+kubebuilder:rbac:groups=ops.local,resources=projects/finalizers,verbs=update

// Reconcile is part of the main kubernetes reconciliation loop which aims to
// move the current state of the cluster closer to the desired state.
// the Project object against the actual cluster state, and then
// perform operations to make the cluster state reflect the state specified by
// the user.
//
// For more details, check Reconcile and its Result here:
// - https://pkg.go.dev/sigs.k8s.io/controller-runtime@v0.17.0/pkg/reconcile
func (r *ProjectReconciler) Reconcile(ctx context.Context, req ctrl.Request) (ctrl.Result, error) {
	log := log.FromContext(ctx)

	// Fetch the Project instance
	// The purpose is check if the Custom Resource for the Kind Project
	// is applied on the cluster if not we return nil to stop the reconciliation
	project := &opsv1alpha1.Project{}
	err := r.Get(ctx, req.NamespacedName, project)
	if err != nil {
		return ctrl.Result{}, client.IgnoreNotFound(err)
	}

	// Let's just set the status as Unknown when no status is available
	if len(project.Status.Conditions) == 0 {
		meta.SetStatusCondition(&project.Status.Conditions, metav1.Condition{
			Type:    typeAvailableProject,
			Status:  metav1.ConditionUnknown,
			Reason:  "Reconciling",
			Message: "Starting reconciliation",
		})
		if err = r.Status().Update(ctx, project); err != nil {
			log.Error(err, "Failed to update Project status")
			return ctrl.Result{}, err
		}

		// Let's re-fetch the project Custom Resource after updating the status
		// so that we have the latest state of the resource on the cluster and we will avoid
		// raising the error "the object has been modified, please apply
		// your changes to the latest version and try again" which would re-trigger the reconciliation
		// if we try to update it again in the following operations
		if err = r.Get(ctx, req.NamespacedName, project); err != nil {
			log.Error(err, "Failed to re-fetch project")
			return ctrl.Result{}, err
		}
	}

	// Let's add a finalizer. Then, we can define some operations which should
	// occur before the custom resource to be deleted.
	// More info: https://kubernetes.io/docs/concepts/overview/working-with-objects/finalizers
	if !controllerutil.ContainsFinalizer(project, ProjectFinalizerName) {
		log.Info("Adding Finalizer for Project")
		if ok := controllerutil.AddFinalizer(project, ProjectFinalizerName); !ok {
			log.Error(err, "Failed to add finalizer into the custom resource")
			return ctrl.Result{Requeue: true}, nil
		}

		if err = r.Update(ctx, project); err != nil {
			log.Error(err, "Failed to update custom resource to add finalizer")
			return ctrl.Result{}, err
		}
	}

	if project.GetDeletionTimestamp() != nil {
		if controllerutil.ContainsFinalizer(project, ProjectFinalizerName) {
			log.Info("Performing Finalizer Operations for Project before delete CR")

			// Let's add here a status "Downgrade" to reflect that this resource began its process to be terminated.
			meta.SetStatusCondition(&project.Status.Conditions, metav1.Condition{Type: typeDegradedProject,
				Status: metav1.ConditionUnknown, Reason: "Finalizing",
				Message: fmt.Sprintf("Performing finalizer operations for the custom resource: %s ", project.Name)})

			if err = r.Status().Update(ctx, project); err != nil {
				log.Error(err, "Failed to update Project status")
				return ctrl.Result{}, err
			}

			if err = r.deleteNamespaces(ctx, project); err != nil {
				log.Error(err, "Failed to delete namespaces")
				return ctrl.Result{Requeue: true}, nil
			}

			if err = r.Get(ctx, req.NamespacedName, project); err != nil {
				log.Error(err, "Failed to re-fetch project")
				return ctrl.Result{}, err
			}

			meta.SetStatusCondition(&project.Status.Conditions, metav1.Condition{
				Type:    typeDegradedProject,
				Status:  metav1.ConditionTrue,
				Reason:  "Finalizing",
				Message: fmt.Sprintf("Finalizer operations for custom resource %s name were successfully accomplished", project.Name),
			})

			if err = r.Status().Update(ctx, project); err != nil {
				log.Error(err, "Failed to update Project status")
				return ctrl.Result{}, err
			}

			log.Info("Removing Finalizer for Project after successfully perform the operations")
			if ok := controllerutil.RemoveFinalizer(project, ProjectFinalizerName); !ok {
				log.Error(err, "Failed to remove finalizer for Project")
				return ctrl.Result{Requeue: true}, nil
			}

			if err = r.Update(ctx, project); err != nil {
				log.Error(err, "Failed to remove finalizer for Project")
				return ctrl.Result{}, err
			}
		}

		return ctrl.Result{}, nil
	}

	if err = r.createNamespaces(ctx, project); err != nil {
		return ctrl.Result{}, goerrors.Join(err, r.Status().Update(ctx, project))
	}

	meta.SetStatusCondition(&project.Status.Conditions, metav1.Condition{
		Type:    typeAvailableProject,
		Status:  metav1.ConditionUnknown,
		Reason:  "Reconciling",
		Message: fmt.Sprintf("Custom resource %s created successfully", project.Name),
	})

	if err = r.Status().Update(ctx, project); err != nil {
		log.Error(err, "Failed to update Project status")
		return ctrl.Result{}, err
	}

	if err = r.Status().Update(ctx, project); err != nil {
		log.Error(err, "Failed to update Project status")
		return ctrl.Result{}, err
	}

	return ctrl.Result{}, nil
}

func (r *ProjectReconciler) createNamespaces(ctx context.Context, project *opsv1alpha1.Project) error {
	for _, environment := range project.Spec.Environments {
		namespaceName := environment.NamespaceName(project.Name)

		existingNamespace := &v1.Namespace{}
		if err := r.Get(ctx, types.NamespacedName{Name: namespaceName}, existingNamespace); err != nil {
			if errors.IsNotFound(err) {
				newNamespace := &v1.Namespace{
					ObjectMeta: metav1.ObjectMeta{
						Name: namespaceName,
					},
				}
				if err = r.Create(ctx, newNamespace); err != nil {
					return err
				}
			} else {
				return err
			}
		}
	}

	return nil
}

func (r *ProjectReconciler) deleteNamespaces(ctx context.Context, project *opsv1alpha1.Project) error {
	for _, environment := range project.Spec.Environments {
		namespaceName := environment.NamespaceName(project.Name)

		existingNamespace := &v1.Namespace{}
		err := r.Get(ctx, types.NamespacedName{Name: namespaceName}, existingNamespace)
		if err != nil {
			if errors.IsNotFound(err) {
				continue
			}
			return err
		}

		if err = r.Delete(ctx, existingNamespace); err != nil {
			return err
		}
	}

	return nil
}

// SetupWithManager sets up the controller with the Manager.
func (r *ProjectReconciler) SetupWithManager(mgr ctrl.Manager) error {
	return ctrl.NewControllerManagedBy(mgr).
		For(&opsv1alpha1.Project{}).
		Complete(r)
}
