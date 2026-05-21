package server

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/stretchr/testify/require"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/watch"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"
	"sigs.k8s.io/controller-runtime/pkg/client/interceptor"

	kargoapi "github.com/akuity/kargo/api/v1alpha1"
	"github.com/akuity/kargo/pkg/server/config"
)

func Test_server_listWarehouses(t *testing.T) {
	testProject := &kargoapi.Project{
		ObjectMeta: metav1.ObjectMeta{Name: "fake-project"},
	}
	testRESTEndpoint(
		t, &config.ServerConfig{},
		http.MethodGet, "/v1beta1/projects/"+testProject.Name+"/warehouses",
		[]restTestCase{
			{
				name: "Project does not exist",
				assertions: func(t *testing.T, w *httptest.ResponseRecorder, _ client.Client) {
					require.Equal(t, http.StatusNotFound, w.Code)
				},
			},
			{
				name:          "no Warehouses exist",
				clientBuilder: fake.NewClientBuilder().WithObjects(testProject),
				assertions: func(t *testing.T, w *httptest.ResponseRecorder, _ client.Client) {
					require.Equal(t, http.StatusOK, w.Code)

					warehouses := &kargoapi.WarehouseList{}
					err := json.Unmarshal(w.Body.Bytes(), warehouses)
					require.NoError(t, err)
					require.Empty(t, warehouses.Items)
				},
			},
			{
				name: "lists Warehouses",
				clientBuilder: fake.NewClientBuilder().WithObjects(
					testProject,
					&kargoapi.Warehouse{
						ObjectMeta: metav1.ObjectMeta{
							Namespace: testProject.Name,
							Name:      "warehouse-1",
						},
					},
					&kargoapi.Warehouse{
						ObjectMeta: metav1.ObjectMeta{
							Namespace: testProject.Name,
							Name:      "warehouse-2",
						},
					},
				),
				assertions: func(t *testing.T, w *httptest.ResponseRecorder, _ client.Client) {
					require.Equal(t, http.StatusOK, w.Code)

					// Examine the Warehouses in the response
					warehouses := &kargoapi.WarehouseList{}
					err := json.Unmarshal(w.Body.Bytes(), warehouses)
					require.NoError(t, err)
					require.Len(t, warehouses.Items, 2)
				},
			},
			{
				name: "sets effective resourceVersion",
				clientBuilder: fake.NewClientBuilder().
					WithObjects(
						testProject,
						&kargoapi.Warehouse{
							ObjectMeta: metav1.ObjectMeta{
								Namespace: testProject.Name,
								Name:      "warehouse-1",
							},
						},
					).
					WithInterceptorFuncs(interceptor.Funcs{
						List: func(
							ctx context.Context,
							cl client.WithWatch,
							list client.ObjectList,
							opts ...client.ListOption,
						) error {
							if err := cl.List(ctx, list, opts...); err != nil {
								return err
							}
							if wl, ok := list.(*kargoapi.WarehouseList); ok {
								wl.ResourceVersion = "0"
								for i := range wl.Items {
									wl.Items[i].ResourceVersion = "100"
								}
							}
							return nil
						},
					}),
				assertions: func(t *testing.T, w *httptest.ResponseRecorder, _ client.Client) {
					require.Equal(t, http.StatusOK, w.Code)
					warehouses := &kargoapi.WarehouseList{}
					err := json.Unmarshal(w.Body.Bytes(), warehouses)
					require.NoError(t, err)
					require.Equal(t, "100", warehouses.ResourceVersion)
				},
			},
		},
	)
}

func Test_server_listWarehouses_watch(t *testing.T) {
	const projectName = "fake-project"

	testRESTWatchEndpoint(
		t, &config.ServerConfig{},
		"/v1beta1/projects/"+projectName+"/warehouses?watch=true",
		[]restWatchTestCase{
			{
				name:          "project not found",
				url:           "/v1beta1/projects/non-existent/warehouses?watch=true",
				clientBuilder: fake.NewClientBuilder(),
				assertions: func(t *testing.T, w *httptest.ResponseRecorder, _ client.Client) {
					require.Equal(t, http.StatusNotFound, w.Code)
				},
			},
			{
				name: "watches all warehouses successfully",
				clientBuilder: fake.NewClientBuilder().WithObjects(
					&kargoapi.Project{
						ObjectMeta: metav1.ObjectMeta{Name: projectName},
					},
					&kargoapi.Warehouse{
						ObjectMeta: metav1.ObjectMeta{
							Namespace: projectName,
							Name:      "warehouse-1",
						},
					},
					&kargoapi.Warehouse{
						ObjectMeta: metav1.ObjectMeta{
							Namespace: projectName,
							Name:      "warehouse-2",
						},
					},
				),
				operations: func(ctx context.Context, c client.Client) {
					// Create a new warehouse to trigger a watch event
					newWarehouse := &kargoapi.Warehouse{
						ObjectMeta: metav1.ObjectMeta{
							Namespace: projectName,
							Name:      "warehouse-3",
						},
					}
					_ = c.Create(ctx, newWarehouse)
				},
				assertions: func(t *testing.T, w *httptest.ResponseRecorder, _ client.Client) {
					require.Equal(t, http.StatusOK, w.Code)

					// Verify SSE headers
					require.Equal(t, "text/event-stream", w.Header().Get("Content-Type"))
					require.Equal(t, "no-cache", w.Header().Get("Cache-Control"))
					require.Equal(t, "keep-alive", w.Header().Get("Connection"))

					// The response body should contain SSE events from the create operation
					body := w.Body.String()
					require.Contains(t, body, "data:")
				},
			},
			{
				name: "watches empty warehouse list",
				clientBuilder: fake.NewClientBuilder().WithObjects(
					&kargoapi.Project{
						ObjectMeta: metav1.ObjectMeta{Name: projectName},
					},
				),
				assertions: func(t *testing.T, w *httptest.ResponseRecorder, _ client.Client) {
					require.Equal(t, http.StatusOK, w.Code)

					// Verify SSE headers are set
					require.Equal(t, "text/event-stream", w.Header().Get("Content-Type"))
				},
			},
			{
				name: "reports expired resourceVersion startup error as SSE error",
				url:  "/v1beta1/projects/" + projectName + "/warehouses?watch=true&resourceVersion=123",
				clientBuilder: fake.NewClientBuilder().
					WithObjects(&kargoapi.Project{
						ObjectMeta: metav1.ObjectMeta{Name: projectName},
					}).
					WithInterceptorFuncs(interceptor.Funcs{
						Watch: func(
							_ context.Context,
							_ client.WithWatch,
							_ client.ObjectList,
							_ ...client.ListOption,
						) (watch.Interface, error) {
							return nil, apierrors.NewResourceExpired("too old resource version: 123")
						},
					}),
				assertions: func(t *testing.T, w *httptest.ResponseRecorder, _ client.Client) {
					require.Equal(t, http.StatusOK, w.Code)
					require.Equal(t, "text/event-stream", w.Header().Get("Content-Type"))
					require.Contains(t, w.Body.String(), "event: error")
					require.Contains(t, w.Body.String(), "watch resource version expired")
				},
			},
		},
	)
}
