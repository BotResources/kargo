package server

import (
	"context"
	"fmt"

	"connectrpc.com/connect"
	"sigs.k8s.io/controller-runtime/pkg/client"

	svcv1alpha1 "github.com/akuity/kargo/api/service/v1alpha1"
	kargoapi "github.com/akuity/kargo/api/v1alpha1"
	"github.com/akuity/kargo/pkg/logging"
)

func (s *server) WatchPromotions(
	ctx context.Context,
	req *connect.Request[svcv1alpha1.WatchPromotionsRequest],
	stream *connect.ServerStream[svcv1alpha1.WatchPromotionsResponse],
) error {
	project := req.Msg.GetProject()
	if err := validateFieldNotEmpty("project", project); err != nil {
		return err
	}

	if err := s.validateProjectExists(ctx, project); err != nil {
		return err
	}

	stage := req.Msg.GetStage()

	if stage != "" && req.Msg.GetResourceVersion() == "" {
		if err := s.client.Get(ctx, client.ObjectKey{
			Namespace: project,
			Name:      stage,
		}, &kargoapi.Stage{}); err != nil {
			return fmt.Errorf("get stage: %w", err)
		}
	}

	w, err := s.client.Watch(
		ctx,
		&kargoapi.PromotionList{},
		buildWatchListOptions(project, req.Msg.GetResourceVersion())...,
	)
	if err != nil {
		return fmt.Errorf("watch promotion: %w", errorFromWatchStartError(err))
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
			if watchErr := errorFromWatchEvent(e); watchErr != nil {
				return watchErr
			}
			promotion, ok := e.Object.(*kargoapi.Promotion)
			if !ok {
				return fmt.Errorf("unexpected object type %T", e.Object)
			}
			eventType := e.Type
			if stage != "" {
				var send bool
				eventType, send = filteredWatchEventType(e.Type, stage == promotion.Spec.Stage)
				if !send {
					continue
				}
			}
			if err = stream.Send(&svcv1alpha1.WatchPromotionsResponse{
				Promotion: promotion,
				Type:      string(eventType),
			}); err != nil {
				return fmt.Errorf("send response: %w", err)
			}
		}
	}
}
