//go:build e2e
// +build e2e

package e2e

import (
	"context"
	"testing"

	eraserv1alpha1 "github.com/Azure/eraser/api/v1alpha1"
	"github.com/Azure/eraser/test/e2e/util"
	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/labels"

	"sigs.k8s.io/e2e-framework/klient/wait"
	"sigs.k8s.io/e2e-framework/klient/wait/conditions"
	"sigs.k8s.io/e2e-framework/pkg/envconf"
	"sigs.k8s.io/e2e-framework/pkg/features"
)

func TestPrune(t *testing.T) {
	pruneImagesFeat := features.New("Prune all non-running images from cluster").
		// Deploy 3 deployments with different images
		// We'll shutdown two of them, run eraser with `*`, then check that the images for the removed deployments are removed from the cluster.
		Setup(func(ctx context.Context, t *testing.T, cfg *envconf.Config) context.Context {
			nginxDep := util.NewDeployment(cfg.Namespace(), util.Nginx, 2, map[string]string{"app": util.Nginx}, corev1.Container{Image: util.Nginx, Name: util.Nginx})
			if err := cfg.Client().Resources().Create(ctx, nginxDep); err != nil {
				t.Error("Failed to create the nginx dep", err)
			}

			util.NewDeployment(cfg.Namespace(), util.Redis, 2, map[string]string{"app": util.Redis}, corev1.Container{Image: util.Redis, Name: util.Redis})
			err := cfg.Client().Resources().Create(ctx, util.NewDeployment(cfg.Namespace(), util.Redis, 2, map[string]string{"app": util.Redis}, corev1.Container{Image: util.Redis, Name: util.Redis}))
			if err != nil {
				t.Fatal(err)
			}

			util.NewDeployment(cfg.Namespace(), util.Caddy, 2, map[string]string{"app": util.Caddy}, corev1.Container{Image: util.Caddy, Name: util.Caddy})
			if err := cfg.Client().Resources().Create(ctx, util.NewDeployment(cfg.Namespace(), util.Caddy, 2, map[string]string{"app": util.Caddy}, corev1.Container{Image: util.Caddy, Name: util.Caddy})); err != nil {
				t.Fatal(err)
			}

			return ctx
		}).
		Assess("Deployments successfully deployed", func(ctx context.Context, t *testing.T, cfg *envconf.Config) context.Context {
			client, err := cfg.NewClient()
			if err != nil {
				t.Error("Failed to create new client", err)
			}

			nginxDep := appsv1.Deployment{
				ObjectMeta: metav1.ObjectMeta{Name: util.Nginx, Namespace: cfg.Namespace()},
			}

			if err = wait.For(conditions.New(client.Resources()).DeploymentConditionMatch(&nginxDep, appsv1.DeploymentAvailable, corev1.ConditionTrue),
				wait.WithTimeout(util.Timeout)); err != nil {
				t.Fatal("nginx deployment not found", err)
			}
			ctx = context.WithValue(ctx, util.Nginx, &nginxDep)

			redisDep := appsv1.Deployment{
				ObjectMeta: metav1.ObjectMeta{Name: util.Redis, Namespace: cfg.Namespace()},
			}
			if err = wait.For(conditions.New(client.Resources()).DeploymentConditionMatch(&redisDep, appsv1.DeploymentAvailable, corev1.ConditionTrue),
				wait.WithTimeout(util.Timeout)); err != nil {
				t.Fatal("redis deployment not found", err)
			}
			ctx = context.WithValue(ctx, util.Redis, &redisDep)

			caddyDep := appsv1.Deployment{
				ObjectMeta: metav1.ObjectMeta{Name: util.Caddy, Namespace: cfg.Namespace()},
			}
			if err = wait.For(conditions.New(client.Resources()).DeploymentConditionMatch(&caddyDep, appsv1.DeploymentAvailable, corev1.ConditionTrue),
				wait.WithTimeout(util.Timeout)); err != nil {
				t.Fatal("caddy deployment not found", err)
			}
			ctx = context.WithValue(ctx, util.Caddy, &caddyDep)

			return ctx
		}).
		Assess("Remove some of the deployments so the images aren't running", func(ctx context.Context, t *testing.T, cfg *envconf.Config) context.Context {
			// Here we remove the redis and caddy deployments
			// Keep nginx running and ensure nginx is not deleted.
			var redisPods corev1.PodList
			if err := cfg.Client().Resources().List(ctx, &redisPods, func(o *metav1.ListOptions) {
				o.LabelSelector = labels.SelectorFromSet(map[string]string{"app": util.Redis}).String()
			}); err != nil {
				t.Fatal(err)
			}
			if len(redisPods.Items) != 2 {
				t.Fatal("missing pods in redis deployment")
			}

			var caddyPods corev1.PodList
			if err := cfg.Client().Resources().List(ctx, &caddyPods, func(o *metav1.ListOptions) {
				o.LabelSelector = labels.SelectorFromSet(map[string]string{"app": util.Caddy}).String()
			}); err != nil {
				t.Fatal(err)
			}
			if len(caddyPods.Items) != 2 {
				t.Fatal("missing pods in caddy deployment")
			}

			err := cfg.Client().Resources().Delete(ctx, ctx.Value(util.Redis).(*appsv1.Deployment))
			if err != nil {
				t.Fatal(err)
			}
			err = cfg.Client().Resources().Delete(ctx, ctx.Value(util.Caddy).(*appsv1.Deployment))
			if err != nil {
				t.Fatal(err)
			}

			for _, nodeName := range util.GetClusterNodes(t) {
				err := wait.For(util.ContainerNotPresentOnNode(nodeName, util.Redis), wait.WithTimeout(util.Timeout))
				if err != nil {
					// Let's not mark this as an error
					// We only have this to prevent race conditions with the eraser spinning up
					t.Logf("error while waiting for deployment deletion: %v", err)
				}
			}
			for _, nodeName := range util.GetClusterNodes(t) {
				err := wait.For(util.ContainerNotPresentOnNode(nodeName, util.Caddy), wait.WithTimeout(util.Timeout))
				if err != nil {
					// Let's not mark this as an error
					// We only have this to prevent race conditions with the eraser spinning up
					t.Logf("error while waiting for deployment deletion: %v", err)
				}
			}
			return ctx
		}).
		Assess("All non-running images are removed from the cluster", func(ctx context.Context, t *testing.T, cfg *envconf.Config) context.Context {
			imgList := &eraserv1alpha1.ImageList{
				ObjectMeta: metav1.ObjectMeta{Name: util.Prune},
				Spec: eraserv1alpha1.ImageListSpec{
					Images: []string{"*"},
				},
			}

			if err := cfg.Client().Resources().Create(ctx, imgList); err != nil {
				t.Fatal(err)
			}
			ctx = context.WithValue(ctx, util.Prune, imgList)

			// The first check could take some extra time, where as things should be done already for the 2nd check.
			// So we'll give plenty of time and fail slow here.
			ctxT, cancel := context.WithTimeout(ctx, util.Timeout)
			defer cancel()
			util.CheckImageRemoved(ctxT, t, util.GetClusterNodes(t), util.Redis)

			ctxT, cancel = context.WithTimeout(ctx, util.Timeout)
			defer cancel()
			util.CheckImageRemoved(ctxT, t, util.GetClusterNodes(t), util.Caddy)

			// Make sure nginx is still there
			util.CheckImagesExist(ctx, t, util.GetClusterNodes(t), util.Nginx)

			return ctx
		}).
		Assess("Get logs", func(ctx context.Context, t *testing.T, cfg *envconf.Config) context.Context {
			if err := util.GetPodLogs(ctx, cfg, t, true); err != nil {
				t.Error("error getting collector pod logs", err)
			}

			if err := util.GetManagerLogs(ctx, cfg, t); err != nil {
				t.Error("error getting manager logs", err)
			}

			return ctx
		}).
		Feature()

	util.Testenv.Test(t, pruneImagesFeat)
}
