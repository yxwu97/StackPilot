package cli

import (
	"context"
	"fmt"
	"net/http"
	"net/url"
	"time"
)

const defaultMetricWindow = 5 * time.Minute

func (run runner) metrics(args []string) int {
	flags, config := newCommandFlags("metrics", run.stderr)
	fromValue := flags.String("from", "", "UTC RFC3339 window start; defaults to five minutes ago")
	toValue := flags.String("to", "", "UTC RFC3339 window end; defaults to now")
	granularity := flags.String("granularity", "detail", "metric granularity: detail or hour")
	serviceID := flags.String("service", "", "optional logical service ID")
	if err := flags.Parse(reorderFlagArgs(args, nil)); err != nil || flags.NArg() > 1 {
		return 2
	}
	start, end, err := metricTimes(*fromValue, *toValue)
	if err != nil || (*granularity != "detail" && *granularity != "hour") {
		return run.report(commandErrorf("metrics requires a valid UTC window and detail or hour granularity"))
	}
	client, code := run.connect(config)
	if code != 0 {
		return code
	}
	defer client.Close()
	workspace, err := resolveWorkspace(run.ctx, client, optionalArgument(flags))
	if err != nil {
		return run.report(err)
	}
	result, err := getMetrics(run.ctx, client, workspace, start, end, *granularity, *serviceID)
	if err != nil {
		return run.report(err)
	}
	return run.write(config.output, result, func() { writeMetricTable(run.stdout, result) })
}

func metricTimes(fromValue, toValue string) (time.Time, time.Time, error) {
	end := time.Now().UTC()
	var err error
	if toValue != "" {
		end, err = parseMetricTime(toValue)
		if err != nil {
			return time.Time{}, time.Time{}, err
		}
	}
	start := end.Add(-defaultMetricWindow)
	if fromValue != "" {
		start, err = parseMetricTime(fromValue)
	}
	if err != nil || !start.Before(end) {
		return time.Time{}, time.Time{}, commandErrorf("invalid metric time window")
	}
	return start, end, nil
}

func parseMetricTime(value string) (time.Time, error) {
	parsed, err := time.Parse(time.RFC3339Nano, value)
	if err != nil {
		return time.Time{}, err
	}
	_, offset := parsed.Zone()
	if offset != 0 {
		return time.Time{}, commandErrorf("metric times must use UTC")
	}
	return parsed.UTC(), nil
}

func getMetrics(ctx context.Context, client *apiClient, workspace workspaceDTO, start, end time.Time, granularity, serviceID string) (metricSeriesListDTO, error) {
	query := url.Values{
		"from":        []string{start.UTC().Format(time.RFC3339Nano)},
		"to":          []string{end.UTC().Format(time.RFC3339Nano)},
		"granularity": []string{granularity},
	}
	if serviceID != "" {
		query.Set("serviceId", serviceID)
	}
	var result metricSeriesListDTO
	path := "/api/v1/workspaces/" + url.PathEscape(workspace.ID) + "/metrics?" + query.Encode()
	err := client.JSON(ctx, http.MethodGet, path, nil, &result)
	return result, err
}

func writeMetricTable(output interface{ Write([]byte) (int, error) }, result metricSeriesListDTO) {
	for _, series := range result.Series {
		if len(series.Points) == 0 {
			continue
		}
		point := series.Points[len(series.Points)-1]
		cpu, memory := "-", "-"
		if point.CPUPercent != nil {
			cpu = fmt.Sprintf("%.2f%%", *point.CPUPercent)
		}
		if point.MemoryBytes != nil {
			memory = fmt.Sprintf("%d", *point.MemoryBytes)
		}
		fmt.Fprintf(output, "%s\t%s\t%s\t%s\t%s\t%s\n", series.ServiceID, series.Source, point.Status, cpu, memory, point.ObservedAt)
	}
}
