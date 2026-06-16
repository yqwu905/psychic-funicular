package server

import (
	"context"

	skipperv1 "github.com/yqwu905/psychic-funicular/gen/skipper/v1"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
)

func (s *clusterService) ListEvents(ctx context.Context, req *skipperv1.ListEventsRequest) (*skipperv1.ListEventsResponse, error) {
	evs, err := s.store.ListEvents(ctx, int(req.GetLimit()))
	if err != nil {
		return nil, status.Error(codes.Internal, "list events failed")
	}
	resp := &skipperv1.ListEventsResponse{Events: make([]*skipperv1.Event, 0, len(evs))}
	for _, e := range evs {
		resp.Events = append(resp.Events, &skipperv1.Event{
			Id:       e.ID,
			Type:     e.Type,
			Severity: string(e.Severity),
			Source:   e.Source,
			Summary:  e.Summary,
			Labels:   e.Labels,
			TimeUnix: unixOrZero(e.Time),
		})
	}
	return resp, nil
}

func (s *clusterService) ListNotifications(ctx context.Context, req *skipperv1.ListNotificationsRequest) (*skipperv1.ListNotificationsResponse, error) {
	ns, err := s.store.ListNotifications(ctx, int(req.GetLimit()))
	if err != nil {
		return nil, status.Error(codes.Internal, "list notifications failed")
	}
	resp := &skipperv1.ListNotificationsResponse{Notifications: make([]*skipperv1.Notification, 0, len(ns))}
	for _, n := range ns {
		resp.Notifications = append(resp.Notifications, &skipperv1.Notification{
			Id:         n.ID,
			EventId:    n.EventID,
			EventType:  n.EventType,
			Rule:       n.Rule,
			Channel:    n.Channel,
			Recipients: n.Recipients,
			Status:     n.Status,
			Error:      n.Error,
			Summary:    n.Summary,
			TimeUnix:   unixOrZero(n.Time),
		})
	}
	return resp, nil
}
