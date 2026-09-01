package storage

import "testing"

func TestObservabilitySchemaEnforcesRevisionPlanAndMetricInvariants(t *testing.T) {
	database := openTestDatabase(t)
	serviceInstanceID := seedRuntimeInstance(t, database)
	const (
		workspaceID         = "ws_01ARZ3NDEKTSV4RRFFQ69G5FAV"
		operationID         = "op_01ARZ3NDEKTSV4RRFFQ69G5FAY"
		runningID           = "rev_01ARZ3NDEKTSV4RRFFQ69G5FAV"
		workspaceRevisionID = "rev_01ARZ3NDEKTSV4RRFFQ69G5FAW"
		now                 = "2026-08-31T12:00:00Z"
	)
	statements := []string{
		`INSERT INTO operations(id,workspace_id,system_id,type,state,idempotency_subject,route_key,request_digest,cancellable,created_at,finished_at) VALUES ('` + operationID + `','` + workspaceID + `','btc','change-plan','succeeded','test','change-plan','aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa',0,'` + now + `','` + now + `')`,
		`INSERT INTO system_revision_snapshots(id,workspace_id,system_id,system_instance_id,kind,schema_version,digest,snapshot_json,created_at) VALUES ('` + runningID + `','` + workspaceID + `','btc','si_01ARZ3NDEKTSV4RRFFQ69G5FAV','running','revision/v1','bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb','{}','` + now + `')`,
		`INSERT INTO system_revision_snapshots(id,workspace_id,system_id,kind,schema_version,digest,snapshot_json,created_at) VALUES ('` + workspaceRevisionID + `','` + workspaceID + `','btc','workspace','revision/v1','cccccccccccccccccccccccccccccccccccccccccccccccccccccccccccccccc','{}','` + now + `')`,
		`INSERT INTO change_plans(id,created_by_operation_id,workspace_id,system_id,from_snapshot_id,to_snapshot_id,rule_version,state,risk,item_count,blocked_count,result_schema_version,result_digest,result_json,created_at) VALUES ('plan_01ARZ3NDEKTSV4RRFFQ69G5FAV','` + operationID + `','` + workspaceID + `','btc','` + runningID + `','` + workspaceRevisionID + `','change-risk/v1','ready','high',1,0,'change-plan/v1','dddddddddddddddddddddddddddddddddddddddddddddddddddddddddddddddd','{}','` + now + `')`,
		`INSERT INTO runtime_metric_samples(service_instance_id,source,status,observed_at,interval_ms,cpu_total_ms,memory_bytes,process_count) VALUES ('` + serviceInstanceID.String() + `','process-job','available','` + now + `',30000,100,2048,2)`,
		`INSERT INTO runtime_metric_samples(service_instance_id,source,status,observed_at,interval_ms,reason_code) VALUES ('` + serviceInstanceID.String() + `','process-job','unsupported','2026-08-31T12:00:30Z',30000,'SUPERVISOR_PROTOCOL_UNSUPPORTED')`,
		`INSERT INTO runtime_metric_hourly_aggregates(service_instance_id,source,bucket_start,sample_count,available_count,cpu_sample_count,cpu_min_percent,cpu_max_percent,cpu_total_percent,memory_sample_count,memory_min_bytes,memory_max_bytes,memory_total_bytes) VALUES ('` + serviceInstanceID.String() + `','process-job','2026-08-31T12:00:00Z',2,1,1,10,10,10,1,2048,2048,2048)`,
	}
	for _, statement := range statements {
		if _, err := database.Exec(statement); err != nil {
			t.Fatalf("insert valid observability row: %v", err)
		}
	}

	invalid := []string{
		`INSERT INTO system_revision_snapshots(id,workspace_id,system_id,kind,schema_version,digest,snapshot_json,created_at) VALUES ('rev_01ARZ3NDEKTSV4RRFFQ69G5FAX','` + workspaceID + `','btc','running','revision/v1','eeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeee','{}','` + now + `')`,
		`INSERT INTO runtime_metric_samples(service_instance_id,source,status,observed_at,interval_ms,cpu_percent,reason_code) VALUES ('` + serviceInstanceID.String() + `','process-job','unavailable','2026-08-31T12:01:00Z',30000,0,'SUPERVISOR_UNAVAILABLE')`,
		`INSERT INTO runtime_metric_hourly_aggregates(service_instance_id,source,bucket_start,sample_count,available_count,cpu_sample_count,cpu_min_percent,memory_sample_count) VALUES ('` + serviceInstanceID.String() + `','process-job','2026-08-31T13:00:00Z',1,1,1,10,0)`,
	}
	for _, statement := range invalid {
		if _, err := database.Exec(statement); err == nil {
			t.Fatal("invalid observability row unexpectedly persisted")
		}
	}
}
