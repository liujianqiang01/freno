/*
   Copyright 2017 GitHub Inc.
	 See https://github.com/github/freno/blob/master/LICENSE
*/

package mysql

import (
	"fmt"
	"strings"
	"time"

	"github.com/outbrain/golib/sqlutils"
	metrics "github.com/rcrowley/go-metrics"
)

type MySQLThrottleMetric struct {
	Key   InstanceKey
	Value float64
	Err   error
}

func NewMySQLThrottleMetric() *MySQLThrottleMetric {
	return &MySQLThrottleMetric{Value: 0}
}

func (metric *MySQLThrottleMetric) Get() (float64, error) {
	return metric.Value, metric.Err
}

// ReadThrottleMetric returns replication lag for a given connection config; either by explicit query
// or via SHOW SLAVE STATUS
func ReadThrottleMetric(probe *Probe) (mySQLThrottleMetric *MySQLThrottleMetric) {
	started := time.Now()
	mySQLThrottleMetric = NewMySQLThrottleMetric()
	mySQLThrottleMetric.Key = probe.Key

	defer func(metric *MySQLThrottleMetric, started time.Time) {
		go func() {
			metrics.GetOrRegisterTimer("probes.latency", nil).Update(time.Since(started))
			metrics.GetOrRegisterCounter("probes.total", nil).Inc(1)
			if metric.Err != nil {
				metrics.GetOrRegisterCounter("probes.error", nil).Inc(1)
			}
		}()
	}(mySQLThrottleMetric, started)

	dbUri := probe.GetDBUri("information_schema")
	db, fromCache, err := sqlutils.GetDB(dbUri)

	if err != nil {
		mySQLThrottleMetric.Err = err
		return mySQLThrottleMetric
	}
	if !fromCache {
		db.SetMaxOpenConns(maxPoolConnections)
		db.SetMaxIdleConns(maxIdleConnections)
	}
	if strings.HasPrefix(strings.ToLower(probe.MetricQuery), "select") {
		mySQLThrottleMetric.Err = db.QueryRow(probe.MetricQuery).Scan(&mySQLThrottleMetric.Value)
		return mySQLThrottleMetric
	}

	if strings.HasPrefix(strings.ToLower(probe.MetricQuery), "show global") {
		var variableName string // just a placeholder
		mySQLThrottleMetric.Err = db.QueryRow(probe.MetricQuery).Scan(&variableName, &mySQLThrottleMetric.Value)
		return mySQLThrottleMetric
	}

	if probe.MetricQuery != "" {
		mySQLThrottleMetric.Err = fmt.Errorf("Unsupported metrics query type: %s", probe.MetricQuery)
		return mySQLThrottleMetric
	}

	// No metric query? By default we look at replication lag as output of SHOW SLAVE STATUS

	mySQLThrottleMetric.Err = sqlutils.QueryRowsMap(db, `show slave status`, func(m sqlutils.RowMap) error {
		slaveIORunning := m.GetString("Slave_IO_Running")
		slaveSQLRunning := m.GetString("Slave_SQL_Running")
		secondsBehindMaster := m.GetNullInt64("Seconds_Behind_Master")
		if !secondsBehindMaster.Valid {
			return fmt.Errorf("replication not running; Slave_IO_Running=%+v, Slave_SQL_Running=%+v", slaveIORunning, slaveSQLRunning)
		}
		mySQLThrottleMetric.Value = float64(secondsBehindMaster.Int64)
		return nil
	})
	return mySQLThrottleMetric
}
