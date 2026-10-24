package telemetry

// PublishCondition accepts only fixed operational kinds, so a new caller cannot
// accidentally turn tenant identifiers or exception text into metric labels.
func PublishCondition(kind string, firing bool) {
	switch kind {
	case "archive_reconciliation", "archive_orphan_original", "telemetry_host_unavailable", "telemetry_queue_unavailable", "telemetry_config_unready", "telemetry_collector_stale", "telemetry_collection_gap", "telemetry_queue_pressure", "telemetry_resource_pressure", "telemetry_certificate_expiring", "telemetry_daemon_postfix_down", "telemetry_daemon_dovecot_down", "telemetry_daemon_nginx_down", "telemetry_daemon_opendkim_down", "telemetry_daemon_nsd_down":
	default:
		return
	}
	value := 0.0
	if firing {
		value = 1
	}
	conditionMetric.WithLabelValues(kind).Set(value)
}
