package cluster

import (
	"context"
	"fmt"
	"time"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
)

// DeletePod deletes a single pod. If it is managed by a controller it will be
// recreated automatically.
func (c *Client) DeletePod(ctx context.Context, namespace, name string) error {
	if c.demo {
		return nil
	}
	if c.docker {
		return c.dockerDelete(ctx, name)
	}
	return c.Clientset.CoreV1().Pods(namespace).Delete(ctx, name, metav1.DeleteOptions{})
}

// deploymentForPod walks pod -> ReplicaSet -> Deployment and returns the
// owning Deployment name, if any.
func (c *Client) deploymentForPod(ctx context.Context, p PodInfo) (string, bool) {
	if p.OwnerKind != "ReplicaSet" || p.OwnerName == "" {
		return "", false
	}
	rs, err := c.Clientset.AppsV1().ReplicaSets(p.Namespace).Get(ctx, p.OwnerName, metav1.GetOptions{})
	if err != nil {
		return "", false
	}
	for _, o := range rs.OwnerReferences {
		if o.Kind == "Deployment" {
			return o.Name, true
		}
	}
	return "", false
}

// RestartWorkload triggers a rollout restart of the pod's Deployment (the same
// mechanism as `kubectl rollout restart`). If the pod is not part of a
// Deployment it falls back to deleting the pod. Returns a human-readable note.
func (c *Client) RestartWorkload(ctx context.Context, p PodInfo) (string, error) {
	if c.demo {
		return "demo: would restart " + p.Controller, nil
	}
	if c.docker {
		return c.dockerRestart(ctx, p.Name)
	}
	if dep, ok := c.deploymentForPod(ctx, p); ok {
		patch := fmt.Sprintf(
			`{"spec":{"template":{"metadata":{"annotations":{"kubeview.dev/restartedAt":%q}}}}}`,
			time.Now().Format(time.RFC3339))
		_, err := c.Clientset.AppsV1().Deployments(p.Namespace).Patch(
			ctx, dep, types.StrategicMergePatchType, []byte(patch), metav1.PatchOptions{})
		if err != nil {
			return "", err
		}
		return "rollout restart deployment/" + dep, nil
	}
	if err := c.DeletePod(ctx, p.Namespace, p.Name); err != nil {
		return "", err
	}
	return "deleted pod (controller will recreate it)", nil
}

// ScaleTarget describes a scalable Deployment behind a pod.
type ScaleTarget struct {
	Namespace string
	Name      string
	Replicas  int32
}

// ScaleInfo resolves the Deployment behind a pod and its current replica count.
func (c *Client) ScaleInfo(ctx context.Context, p PodInfo) (ScaleTarget, bool) {
	if c.docker {
		return ScaleTarget{}, false // no replica concept for a plain container
	}
	if c.demo {
		if len(p.Controller) > 11 && p.Controller[:11] == "Deployment/" {
			return ScaleTarget{Namespace: p.Namespace, Name: p.Controller[11:], Replicas: 2}, true
		}
		return ScaleTarget{}, false
	}
	dep, ok := c.deploymentForPod(ctx, p)
	if !ok {
		return ScaleTarget{}, false
	}
	s, err := c.Clientset.AppsV1().Deployments(p.Namespace).GetScale(ctx, dep, metav1.GetOptions{})
	if err != nil {
		return ScaleTarget{}, false
	}
	return ScaleTarget{Namespace: p.Namespace, Name: dep, Replicas: s.Spec.Replicas}, true
}

// Scale sets the replica count of a Deployment.
func (c *Client) Scale(ctx context.Context, namespace, deployment string, replicas int32) error {
	if c.demo {
		return nil
	}
	s, err := c.Clientset.AppsV1().Deployments(namespace).GetScale(ctx, deployment, metav1.GetOptions{})
	if err != nil {
		return err
	}
	s.Spec.Replicas = replicas
	_, err = c.Clientset.AppsV1().Deployments(namespace).UpdateScale(ctx, deployment, s, metav1.UpdateOptions{})
	return err
}
