package server

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/stretchr/testify/require"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/client-go/rest"
	"sigs.k8s.io/controller-runtime/pkg/client"

	"github.com/akuity/kargo/api/service/v1alpha1/svcv1alpha1connect"
	"github.com/akuity/kargo/pkg/server/kubernetes"
	"github.com/akuity/kargo/pkg/server/validation"
)

const watchTestProjectName = "fake-project"

// newWatchTestClient returns a Connect client backed by a test server whose
// Kubernetes client uses the provided internal client.
func newWatchTestClient(
	t *testing.T,
	internalClient client.WithWatch,
) svcv1alpha1connect.KargoServiceClient {
	k8sClient, err := kubernetes.NewClient(
		t.Context(),
		&rest.Config{},
		kubernetes.ClientOptions{
			SkipAuthorization: true,
			NewInternalClient: func(
				context.Context,
				*rest.Config,
				*runtime.Scheme,
				string,
			) (client.WithWatch, error) {
				return internalClient, nil
			},
		},
	)
	require.NoError(t, err)

	svr := &server{client: k8sClient}
	svr.externalValidateProjectFn = func(_ context.Context, _ client.Client, project string) error {
		if project != watchTestProjectName {
			return validation.ErrProjectNotFound
		}
		return nil
	}

	mux := http.NewServeMux()
	mux.Handle(svcv1alpha1connect.NewKargoServiceHandler(svr))
	httpSrv := httptest.NewServer(mux)
	t.Cleanup(httpSrv.Close)

	return svcv1alpha1connect.NewKargoServiceClient(httpSrv.Client(), httpSrv.URL)
}

// resourceVersionFromListOptions extracts the raw ResourceVersion from
// controller-runtime list options.
func resourceVersionFromListOptions(opts ...client.ListOption) string {
	var listOpts client.ListOptions
	for _, opt := range opts {
		opt.ApplyToList(&listOpts)
	}
	if listOpts.Raw == nil {
		return ""
	}
	return listOpts.Raw.ResourceVersion
}
