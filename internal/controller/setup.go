/*
Copyright 2026 The image-build-controller Authors.

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
	"errors"
	"fmt"

	batchv1 "k8s.io/api/batch/v1"
	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/labels"
	"k8s.io/apimachinery/pkg/types"
	"sigs.k8s.io/controller-runtime/pkg/cache"
	"sigs.k8s.io/controller-runtime/pkg/client"

	"github.com/leightweight/image-build-controller/internal/build"
)

// CacheOptions limits the manager's Job and pod caches to build Jobs and pods,
// so the controller doesn't hold every Job and pod in the cluster in memory.
func CacheOptions() cache.Options {
	selector := labels.SelectorFromSet(labels.Set{build.ManagedByLabel: build.ManagedByValue})
	return cache.Options{
		ByObject: map[client.Object]cache.ByObject{
			&batchv1.Job{}: {Label: selector},
			&corev1.Pod{}:  {Label: selector},
		},
	}
}

// ResolveOwnImage returns the image of a container in the controller's own pod.
// The build Job's fetch init container runs the controller image, and this finds it without configuration.
func ResolveOwnImage(ctx context.Context, c client.Reader, namespace, name, container string) (string, error) {
	if namespace == "" || name == "" {
		return "", errors.New("the POD_NAMESPACE and POD_NAME environment variables are required to find the controller image; " +
			"set them with the downward API, or set --fetch-image")
	}
	pod := &corev1.Pod{}
	if err := c.Get(ctx, types.NamespacedName{Namespace: namespace, Name: name}, pod); err != nil {
		return "", fmt.Errorf("could not get the controller pod %s/%s: %w", namespace, name, err)
	}
	for _, ct := range pod.Spec.Containers {
		if ct.Name == container {
			return ct.Image, nil
		}
	}
	return "", fmt.Errorf("the controller pod %s/%s has no container named %q", namespace, name, container)
}
