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
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/types"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/log"
	"slices"
)

const (
	finalizerID = "project-operator"
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
	logger := log.FromContext(ctx)
	logger.Info("Reconcile called")

	project := &opsv1alpha1.Project{}
	if err := r.Get(ctx, req.NamespacedName, project); err != nil {
		return ctrl.Result{}, client.IgnoreNotFound(err)
	}

	if !project.ObjectMeta.DeletionTimestamp.IsZero() {
		logger.Info("Project marked for deletion")

		if slices.Contains(project.ObjectMeta.Finalizers, finalizerID) {
			if err := r.deleteNamespaces(ctx, project); err != nil {
				return ctrl.Result{}, fmt.Errorf("could not delete namespaces: %w", err)
			}

			// Remove our finalizer from the list and update it.
			project.ObjectMeta.Finalizers = slices.DeleteFunc(project.ObjectMeta.Finalizers, func(s string) bool {
				return s == finalizerID
			})

			if err := r.Update(ctx, project); err != nil {
				return ctrl.Result{}, fmt.Errorf("could not remove finalizer: %w", err)
			}
		}

		return ctrl.Result{}, nil
	}

	// register our finalizer if it does not exist
	if !slices.Contains(project.ObjectMeta.Finalizers, finalizerID) {
		project.ObjectMeta.Finalizers = append(project.ObjectMeta.Finalizers, finalizerID)
		if err := r.Update(ctx, project); err != nil {
			return ctrl.Result{}, fmt.Errorf("could not add finalizer: %w", err)
		}
	}

	if err := r.createNamespaces(ctx, project); err != nil {
		project.Status.Phase = opsv1alpha1.ErrorProjectStatusPhase

		return ctrl.Result{}, goerrors.Join(err, r.Status().Update(ctx, project))
	}

	if err := r.Status().Update(ctx, project); err != nil {
		return ctrl.Result{}, fmt.Errorf("could not update status: %w", err)
	}

	return ctrl.Result{}, nil
}

func (r *ProjectReconciler) createNamespaces(ctx context.Context, project *opsv1alpha1.Project) error {
	logger := log.FromContext(ctx)

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
				if err := r.Create(ctx, newNamespace); err != nil {
					return err
				}

				logger.Info("Namespace created", "namespace", namespaceName)
			} else {
				return err
			}
		}

		project.Status.Phase = opsv1alpha1.SuccessProjectStatusPhase
	}

	return nil
}

func (r *ProjectReconciler) deleteNamespaces(ctx context.Context, project *opsv1alpha1.Project) error {
	logger := log.FromContext(ctx)

	for _, environment := range project.Spec.Environments {
		namespaceName := environment.NamespaceName(project.Name)

		existingNamespace := &v1.Namespace{}
		if err := r.Get(ctx, types.NamespacedName{Name: namespaceName}, existingNamespace); err != nil {
			if !errors.IsNotFound(err) {
				return err
			}
		}

		if err := r.Delete(ctx, existingNamespace); err != nil {
			return err
		}

		logger.Info("Namespace deleted", "namespace", namespaceName)
	}

	return nil
}

// SetupWithManager sets up the controller with the Manager.
func (r *ProjectReconciler) SetupWithManager(mgr ctrl.Manager) error {
	return ctrl.NewControllerManagedBy(mgr).
		For(&opsv1alpha1.Project{}).
		Complete(r)
}
