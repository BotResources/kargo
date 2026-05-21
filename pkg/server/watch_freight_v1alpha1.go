package server

import (
	"context"
	"fmt"
	"slices"

	"connectrpc.com/connect"

	svcv1alpha1 "github.com/akuity/kargo/api/service/v1alpha1"
	kargoapi "github.com/akuity/kargo/api/v1alpha1"
	"github.com/akuity/kargo/pkg/logging"
)

func (s *server) WatchFreight(
	ctx context.Context,
	req *connect.Request[svcv1alpha1.WatchFreightRequest],
	stream *connect.ServerStream[svcv1alpha1.WatchFreightResponse],
) error {
	project := req.Msg.GetProject()
	if err := validateFieldNotEmpty("project", project); err != nil {
		return err
	}

	if err := s.validateProjectExists(ctx, project); err != nil {
		return err
	}

	warehouses := req.Msg.GetOrigins()

	w, err := s.client.Watch(
		ctx,
		&kargoapi.FreightList{},
		buildWatchListOptions(project, req.Msg.GetResourceVersion())...,
	)
	if err != nil {
		return fmt.Errorf("watch freight: %w", errorFromWatchStartError(err))
	}
	defer w.Stop()

	for {
		select {
		case <-ctx.Done():
			logger := logging.LoggerFromContext(ctx)
			logger.Debug(ctx.Err().Error())
			return nil
		case e, ok := <-w.ResultChan():
			if !ok {
				return nil
			}
			if err := errorFromWatchEvent(e); err != nil {
				return err
			}
			freight, ok := e.Object.(*kargoapi.Freight)
			if !ok {
				return fmt.Errorf("unexpected object type %T", e.Object)
			}
			eventType := e.Type
			if len(warehouses) > 0 {
				var send bool
				eventType, send = filteredWatchEventType(
					e.Type,
					slices.Contains(warehouses, freight.Origin.Name),
				)
				if !send {
					continue
				}
			}
			if err := stream.Send(&svcv1alpha1.WatchFreightResponse{
				Freight: freight,
				Type:    string(eventType),
			}); err != nil {
				return fmt.Errorf("send response: %w", err)
			}
		}
	}
}
