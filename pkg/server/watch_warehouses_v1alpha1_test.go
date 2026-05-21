package server

import (
	"context"
	"testing"
	"time"

	"connectrpc.com/connect"
	"github.com/stretchr/testify/require"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/watch"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"
	"sigs.k8s.io/controller-runtime/pkg/client/interceptor"

	svcv1alpha1 "github.com/akuity/kargo/api/service/v1alpha1"
	kargoapi "github.com/akuity/kargo/api/v1alpha1"
)

func TestWatchWarehouses_resourceVersion(t *testing.T) {
	t.Parallel()

	const projectName = watchTestProjectName

	resourceVersionCh := make(chan string, 1)

	fakeClient := fake.NewClientBuilder().
		WithScheme(mustNewScheme()).
		WithInterceptorFuncs(interceptor.Funcs{
			Watch: func(
				_ context.Context,
				_ client.WithWatch,
				_ client.ObjectList,
				opts ...client.ListOption,
			) (watch.Interface, error) {
				resourceVersionCh <- resourceVersionFromListOptions(opts...)

				w := watch.NewFake()
				go func() {
					time.Sleep(10 * time.Millisecond)
					w.Add(&kargoapi.Warehouse{
						ObjectMeta: metav1.ObjectMeta{
							Namespace: projectName,
							Name:      "warehouse-1",
						},
					})
				}()
				return w, nil
			},
		}).
		Build()

	cli := newWatchTestClient(t, fakeClient)

	ctx, cancel := context.WithTimeout(t.Context(), 2*time.Second)
	defer cancel()

	stream, err := cli.WatchWarehouses(ctx, connect.NewRequest(&svcv1alpha1.WatchWarehousesRequest{
		Project:         projectName,
		Name:            "warehouse-1",
		ResourceVersion: "123",
	}))
	require.NoError(t, err)
	require.True(t, stream.Receive())
	require.Equal(t, "warehouse-1", stream.Msg().GetWarehouse().GetName())

	select {
	case rv := <-resourceVersionCh:
		require.Equal(t, "123", rv)
	default:
		require.Fail(t, "watch was not called")
	}
}

func TestWatchWarehouses_expiredResourceVersionOnStart(t *testing.T) {
	t.Parallel()

	const projectName = watchTestProjectName

	fakeClient := fake.NewClientBuilder().
		WithScheme(mustNewScheme()).
		WithInterceptorFuncs(interceptor.Funcs{
			Watch: func(
				context.Context,
				client.WithWatch,
				client.ObjectList,
				...client.ListOption,
			) (watch.Interface, error) {
				return nil, apierrors.NewResourceExpired("too old resource version: 123")
			},
		}).
		Build()

	cli := newWatchTestClient(t, fakeClient)

	ctx, cancel := context.WithTimeout(t.Context(), 2*time.Second)
	defer cancel()

	stream, err := cli.WatchWarehouses(ctx, connect.NewRequest(&svcv1alpha1.WatchWarehousesRequest{
		Project:         projectName,
		ResourceVersion: "123",
	}))
	require.NoError(t, err)
	require.False(t, stream.Receive())
	require.Error(t, stream.Err())
	require.Equal(t, connect.CodeOutOfRange, connect.CodeOf(stream.Err()))
	require.ErrorContains(t, stream.Err(), "watch resource version expired")
}
