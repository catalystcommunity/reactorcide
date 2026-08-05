package uiapi

import (
	"context"
	"errors"
	"time"

	"github.com/catalystcommunity/reactorcide/coordinator_api/internal/jobtelemetry"
	"github.com/catalystcommunity/reactorcide/coordinator_api/internal/uiapi/csilapi"
)

// GetJobMetrics returns graph-ready metrics after it applies the job's
// project and organization visibility rule to the caller's UI identity.
func (s *UiService) GetJobMetrics(ctx context.Context, req csilapi.GetJobMetricsRequest) (csilapi.GetJobMetricsResponse, error) {
	identity, _ := s.deps.resolveIdentity(ctx)
	job, err := s.deps.Store.GetJobByID(ctx, req.JobId)
	if err != nil {
		return csilapi.GetJobMetricsResponse{}, mapStoreErr(err, "job not found")
	}
	visible, err := s.deps.Resolver.CanViewJob(ctx, identity, job)
	if err != nil {
		return csilapi.GetJobMetricsResponse{}, NewServiceError("internal", "an internal error occurred")
	}
	if !visible {
		return csilapi.GetJobMetricsResponse{}, NewServiceError("forbidden", "you do not have permission to view this job")
	}
	var fromValue, toValue string
	if req.FromTime != nil {
		fromValue = *req.FromTime
	}
	if req.ToTime != nil {
		toValue = *req.ToTime
	}
	from, err := jobtelemetry.ParseOptionalTime(fromValue)
	if err != nil {
		return csilapi.GetJobMetricsResponse{}, NewServiceError("invalid_argument", "from_time must be an RFC3339 timestamp")
	}
	to, err := jobtelemetry.ParseOptionalTime(toValue)
	if err != nil {
		return csilapi.GetJobMetricsResponse{}, NewServiceError("invalid_argument", "to_time must be an RFC3339 timestamp")
	}
	if from != nil && to != nil && from.After(*to) {
		return csilapi.GetJobMetricsResponse{}, NewServiceError("invalid_argument", "from_time must not be after to_time")
	}
	var cursor string
	if req.Cursor != nil {
		cursor = *req.Cursor
	}
	result, err := jobtelemetry.QueryMetrics(ctx, s.deps.ObjectStore, jobtelemetry.Query{
		JobID:     job.JobID,
		From:      from,
		To:        to,
		Metrics:   req.Metrics,
		MaxPoints: int(req.MaxPoints),
		Cursor:    cursor,
	})
	if err != nil {
		if errors.Is(err, jobtelemetry.ErrInvalidCursor) {
			return csilapi.GetJobMetricsResponse{}, NewServiceError("invalid_argument", "cursor is invalid for this metric query")
		}
		return csilapi.GetJobMetricsResponse{}, NewServiceError("internal", "failed to read job metrics")
	}
	nextCursor := result.NextCursor
	response := csilapi.GetJobMetricsResponse{Complete: result.Complete, NextCursor: &nextCursor}
	for _, series := range result.Series {
		converted := csilapi.JobMetricSeries{Name: series.Name, Unit: series.Unit}
		for _, label := range series.Labels {
			converted.Labels = append(converted.Labels, csilapi.JobMetricLabel{Key: label.Key, Value: label.Value})
		}
		for _, point := range series.Points {
			converted.Points = append(converted.Points, csilapi.JobMetricPoint{
				ObservedAt: point.ObservedAt.UTC().Format(time.RFC3339Nano),
				Value:      point.Value,
				Min:        point.Min,
				Max:        point.Max,
			})
		}
		response.Series = append(response.Series, converted)
	}
	for _, unavailable := range result.Unavailable {
		response.Unavailable = append(response.Unavailable, csilapi.JobMetricUnavailable{
			MetricPrefix: unavailable.MetricPrefix,
			Reason:       unavailable.Reason,
		})
	}
	return response, nil
}
