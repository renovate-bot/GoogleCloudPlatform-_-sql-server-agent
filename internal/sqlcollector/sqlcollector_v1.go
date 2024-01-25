/*
Copyright 2023 Google LLC

Licensed under the Apache License, Version 2.0 (the "License");
you may not use this file except in compliance with the License.
You may obtain a copy of the License at

    https://www.apache.org/licenses/LICENSE-2.0

Unless required by applicable law or agreed to in writing, software
distributed under the License is distributed on an "AS IS" BASIS,
WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
See the License for the specific language governing permissions and
limitations under the License.
*/

package sqlcollector

import (
	"context"
	"database/sql"
	"time"

	"github.com/GoogleCloudPlatform/sapagent/shared/log"
	"github.com/GoogleCloudPlatform/sql-server-agent/internal"
)

// V1 that execute cmd and connect to SQL server.
type V1 struct {
	dbConn  *sql.DB
	windows bool
}

// NewV1 initializes a V1 instance.
func NewV1(driver, conn string, windows bool) (*V1, error) {
	dbConn, err := sql.Open(driver, conn)
	if err != nil {
		return nil, err
	}
	return &V1{dbConn: dbConn, windows: windows}, nil
}

// CollectMasterRules collects master rules from target sql server.
// Master rules are defined in rules.go file.
func (c *V1) CollectMasterRules(ctx context.Context, timeout time.Duration) []internal.Details {
	details := []internal.Details{}
	for _, rule := range internal.MasterRules {
		func() {
			ctxWithTimeout, cancel := context.WithTimeout(ctx, timeout)
			defer cancel()
			queryResult, err := c.executeSQL(ctxWithTimeout, rule.Query)
			if err != nil {
				log.Logger.Errorw("Failed to run sql query", "query", rule.Query, "error", err)
				return
			}
			details = append(details, internal.Details{
				Name:   rule.Name,
				Fields: rule.Fields(queryResult),
			})
		}()
	}
	return details
}

// Close closes the database collection.
func (c *V1) Close() error {
	return c.dbConn.Close()
}

func (c *V1) executeSQL(ctx context.Context, query string) ([][]any, error) {
	err := c.dbConn.PingContext(ctx)
	if err != nil {
		return nil, err
	}

	// Execute query
	rows, err := c.dbConn.QueryContext(ctx, query)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	cols, err := rows.Columns()
	if err != nil {
		return nil, err
	}

	width := len(cols)
	var res [][]any
	// Iterate through the result set.
	for rows.Next() {
		row := make([]any, width)
		ptrs := make([]any, width)
		for i := range row {
			ptrs[i] = &row[i]
		}
		if err := rows.Scan(ptrs...); err != nil {
			return nil, err
		}
		res = append(res, row)

	}
	return res, nil
}
