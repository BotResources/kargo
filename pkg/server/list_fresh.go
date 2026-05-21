package server

import (
	"context"
	"fmt"

	"k8s.io/client-go/rest"
	"sigs.k8s.io/controller-runtime/pkg/client"

	kargoapi "github.com/akuity/kargo/api/v1alpha1"
)

// directClientFactory creates uncached Kubernetes clients for listFresh. Tests
// use it to exercise the direct-list path without reaching a real API server.
type directClientFactory func(*rest.Config, client.Options) (client.Client, error)

// listFresh lists resources directly from the Kubernetes API server when a
// rest.Config is available. The API server's cached client can return a list
// ResourceVersion of "0", which causes follow-up watches to replay existing
// objects. This helper authorizes the list first, then bypasses the cache only
// for the actual read. Callers should pass options supported by direct API
// lists; cache-only field selectors do not belong here.
func (s *server) listFresh(
	ctx context.Context,
	resource string,
	list client.ObjectList,
	opts ...client.ListOption,
) error {
	if s.cfg.RestConfig == nil {
		if s.client == nil {
			return fmt.Errorf("kubernetes client is not configured")
		}
		return s.client.List(ctx, list, opts...)
	}

	var listOpts client.ListOptions
	listOpts.ApplyOptions(opts)
	if s.authorizeFn == nil {
		return fmt.Errorf("authorize function is not configured")
	}
	if err := s.authorizeFn(
		ctx,
		"list",
		kargoapi.GroupVersion.WithResource(resource),
		"",
		client.ObjectKey{Namespace: listOpts.Namespace},
	); err != nil {
		return err
	}

	newDirectClientFn := s.newDirectClientFn
	if newDirectClientFn == nil {
		newDirectClientFn = client.New
	}
	directClient, err := newDirectClientFn(
		s.cfg.RestConfig,
		client.Options{
			Scheme: s.client.Scheme(),
			Mapper: s.client.RESTMapper(),
		},
	)
	if err != nil {
		return fmt.Errorf("failed to create direct Kubernetes client: %w", err)
	}
	return directClient.List(ctx, list, opts...)
}
