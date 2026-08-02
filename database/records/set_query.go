package records

import (
	"context"
	"fmt"
	"strings"
	"time"

	"github.com/komari-monitor/komari/database/models"
	"gorm.io/gorm"
)

// SetQueryResult exposes the constant query count so operational report tests
// can lock down the absence of per-client database access.
type SetQueryResult struct {
	Records    []models.Record
	SQLQueries int
}

// QueryRecordsForClients reads a whole authorized client set with one SQL
// statement. A nil client set means all clients; a non-nil empty set means no
// clients. loadType is resolved through the same fixed projection allowlist as
// the public history APIs.
func QueryRecordsForClients(ctx context.Context, db *gorm.DB, clientIDs []string, start, end time.Time, loadType string) (SetQueryResult, error) {
	return queryRecordsForClientsAt(ctx, db, clientIDs, start, end, loadType, time.Now())
}

func queryRecordsForClientsAt(ctx context.Context, db *gorm.DB, clientIDs []string, start, end time.Time, loadType string, now time.Time) (SetQueryResult, error) {
	result := SetQueryResult{}
	if db == nil {
		return result, fmt.Errorf("record database is required")
	}
	if ctx == nil {
		return result, fmt.Errorf("record query context is required")
	}
	if start.IsZero() || end.IsZero() || !end.After(start) {
		return result, ErrInvalidQueryRange
	}
	if clientIDs != nil && len(clientIDs) == 0 {
		result.Records = []models.Record{}
		return result, nil
	}
	projection, err := RecordProjection(loadType)
	if err != nil {
		return result, err
	}
	segments, err := PlanRecordQuery(start, end, now, adminQueryBudget.MaxPoints)
	if err != nil {
		return result, err
	}
	parts := make([]string, 0, len(segments))
	args := make([]any, 0, len(segments)*(2+len(clientIDs)))
	filterInSQL := len(clientIDs) > 0 && len(clientIDs) <= maxTrafficSQLClientFilter
	for _, segment := range segments {
		part := fmt.Sprintf("SELECT %s FROM %s WHERE time >= ? AND time < ?", projection, segment.Table)
		args = append(args, models.FromTime(segment.Start), models.FromTime(segment.End))
		if filterInSQL {
			part += " AND client IN (" + strings.TrimSuffix(strings.Repeat("?,", len(clientIDs)), ",") + ")"
			for _, clientID := range clientIDs {
				args = append(args, clientID)
			}
		}
		parts = append(parts, part)
	}
	statement := "SELECT * FROM (" + strings.Join(parts, " UNION ALL ") + ") ORDER BY client ASC,time ASC"
	result.SQLQueries = 1
	if err := db.WithContext(ctx).Raw(statement, args...).Scan(&result.Records).Error; err != nil {
		return result, err
	}
	if err := ctx.Err(); err != nil {
		return result, err
	}
	if !filterInSQL && len(clientIDs) > 0 {
		allowed := make(map[string]struct{}, len(clientIDs))
		for _, clientID := range clientIDs {
			allowed[clientID] = struct{}{}
		}
		filtered := result.Records[:0]
		for _, record := range result.Records {
			if _, ok := allowed[record.Client]; ok {
				filtered = append(filtered, record)
			}
		}
		result.Records = filtered
	}
	return result, nil
}
