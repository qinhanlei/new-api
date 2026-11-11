package model

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"strings"

	"github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/pkg/billingexpr"
	"gorm.io/gorm"
)

// CNY history stays with its funds in the main database, independently of
// consume-log settings and the availability of a separate log database.
func cnyLogQuery(db *gorm.DB, userID *int, filter LogFilter) (*gorm.DB, error) {
	tx := db.Model(&CNYBillingRecord{}).Where("currency = ?", billingexpr.CurrencyCNY)
	if userID != nil {
		tx = tx.Where("user_id = ?", *userID)
	}
	switch filter.Type {
	case LogTypeUnknown:
		tx = tx.Where("kind IN ?", []string{CNYBillingRequest, CNYBillingAdjustment})
	case LogTypeConsume:
		tx = tx.Where("kind = ? AND status <> ?", CNYBillingRequest, CNYBillingRefunded)
	case LogTypeRefund:
		tx = tx.Where("kind = ? AND status = ?", CNYBillingRequest, CNYBillingRefunded)
	case LogTypeManage:
		tx = tx.Where("kind = ?", CNYBillingAdjustment)
	default:
		tx = tx.Where("1 = 0")
	}
	for _, field := range []struct{ column, value string }{{"model_name", filter.ModelName}, {"username", filter.Username}} {
		if field.value == "" {
			continue
		}
		if strings.Contains(field.value, "%") {
			pattern, err := sanitizeLikePattern(field.value)
			if err != nil {
				return nil, err
			}
			tx = tx.Where(field.column+" LIKE ? ESCAPE '!'", pattern)
		} else {
			tx = tx.Where(field.column+" = ?", field.value)
		}
	}
	if filter.TokenName != "" {
		tx = tx.Where("token_name = ?", filter.TokenName)
	}
	if filter.RequestId != "" {
		tx = tx.Where("request_id = ?", filter.RequestId)
	}
	if filter.UpstreamRequestId != "" {
		// Upstream request IDs are not persisted here. Ignoring the filter
		// would misleadingly return unrelated requests.
		tx = tx.Where("1 = 0")
	}
	if filter.StartTimestamp != 0 {
		tx = tx.Where("created_at >= ?", filter.StartTimestamp)
	}
	if filter.EndTimestamp != 0 {
		tx = tx.Where("created_at <= ?", filter.EndTimestamp)
	}
	if filter.Channel != 0 {
		tx = tx.Where("channel_id = ?", filter.Channel)
	}
	if filter.Group != "" {
		tx = tx.Where(commonGroupCol+" = ?", filter.Group)
	}
	return tx, nil
}

func (record *CNYBillingRecord) usageLog() (*Log, error) {
	other := make(map[string]json.RawMessage)
	if record.Other != "" {
		// Preserve untouched raw values, including large integer diagnostics.
		if err := common.UnmarshalJsonStr(record.Other, &other); err != nil || other == nil {
			other = make(map[string]json.RawMessage)
		}
	}
	status := record.Status
	if status == CNYBillingSubmitted {
		status = CNYBillingReserved
	}
	metadata := map[string]any{
		"currency": billingexpr.CurrencyCNY, "billing_status": status,
		"quota_per_unit": record.QuotaPerUnit, "reserved_quota": 0,
	}
	if status == CNYBillingReserved {
		metadata["reserved_quota"] = record.ReservedQuota
	}
	if record.TaskID != "" {
		metadata["task_id"] = record.TaskID
	}
	var snapshot billingexpr.BillingSnapshot
	if record.Snapshot != "" && common.UnmarshalJsonStr(record.Snapshot, &snapshot) == nil {
		fallback := map[string]any{"matched_tier": snapshot.EstimatedTier, "group_ratio": snapshot.GroupRatio}
		if snapshot.BillingRate != nil {
			fallback["billing_rate"] = *snapshot.BillingRate
		}
		if snapshot.ExprString != "" {
			fallback["billing_mode"] = snapshot.BillingMode
			fallback["expr_b64"] = base64.StdEncoding.EncodeToString([]byte(snapshot.ExprString))
		}
		if snapshot.UsageFacts != nil {
			fallback["usage_facts"] = snapshot.UsageFacts
		}
		for key, value := range fallback {
			if _, exists := other[key]; !exists {
				metadata[key] = value
			}
		}
	}
	log := &Log{
		Currency: billingexpr.CurrencyCNY, Id: int(record.ID), UserId: record.UserID, CreatedAt: record.CreatedAt,
		Type: LogTypeConsume, Content: record.Reason, Username: record.Username, TokenName: record.TokenName,
		ModelName: record.ModelName, PromptTokens: record.PromptTokens, CompletionTokens: record.CompletionTokens,
		UseTime: record.UseTime, IsStream: record.IsStream, ChannelId: record.ChannelID, TokenId: record.TokenID,
		Group: record.Group, RequestId: record.RequestID,
	}
	switch {
	case record.Kind == CNYBillingAdjustment:
		log.Type, log.Quota = LogTypeManage, record.DeltaQuota
		metadata["delta_quota"], metadata["before_quota"], metadata["after_quota"] = record.DeltaQuota, record.BeforeQuota, record.AfterQuota
		metadata["reason"] = record.Reason
	case record.Status == CNYBillingRefunded:
		log.Type = LogTypeRefund
	case record.Status == CNYBillingSettled:
		log.Quota = record.Quota
	}
	for key, value := range metadata {
		encoded, err := common.Marshal(value)
		if err != nil {
			return nil, err
		}
		other[key] = encoded
	}
	encoded, err := common.Marshal(other)
	if err != nil {
		return nil, err
	}
	log.Other = string(encoded)
	return log, nil
}

func populateCNYLogChannels(logs []*Log) error {
	ids := make(map[int]bool)
	for _, log := range logs {
		if log.ChannelId != 0 {
			ids[log.ChannelId] = true
		}
	}
	if len(ids) == 0 {
		return nil
	}
	channelIDs := make([]int, 0, len(ids))
	for id := range ids {
		channelIDs = append(channelIDs, id)
	}
	var channels []Channel
	if err := DB.Select("id", "name").Where("id IN ?", channelIDs).Find(&channels).Error; err != nil {
		return err
	}
	names := make(map[int]string, len(channels))
	for _, channel := range channels {
		names[channel.Id] = channel.Name
	}
	for _, log := range logs {
		log.ChannelName = names[log.ChannelId]
	}
	return nil
}

func getCNYLogs(userID *int, filter LogFilter, startIdx, num int) ([]*Log, int64, error) {
	tx, err := cnyLogQuery(DB, userID, filter)
	if err != nil {
		return nil, 0, err
	}
	var total int64
	if err = tx.Count(&total).Error; err != nil {
		return nil, 0, err
	}
	var records []CNYBillingRecord
	if err = tx.Order("created_at DESC, id DESC").Offset(startIdx).Limit(num).Find(&records).Error; err != nil {
		return nil, 0, err
	}
	logs := make([]*Log, 0, len(records))
	for _, record := range records {
		log, err := record.usageLog()
		if err != nil {
			return nil, 0, err
		}
		logs = append(logs, log)
	}
	if userID == nil {
		if err = populateCNYLogChannels(logs); err != nil {
			return nil, 0, err
		}
	} else {
		formatUserLogs(logs, startIdx)
	}
	return logs, total, nil
}

func GetAllCNYLogs(filter LogFilter, startIdx, num int) ([]*Log, int64, error) {
	return getCNYLogs(nil, filter, startIdx, num)
}

func GetUserCNYLogs(userID int, filter LogFilter, startIdx, num int) ([]*Log, int64, error) {
	return getCNYLogs(&userID, filter, startIdx, num)
}

func streamCNYLogs(ctx context.Context, userID *int, filter LogFilter, role int, fn func(*Log) error) error {
	tx, err := cnyLogQuery(DB.WithContext(ctx), userID, filter)
	if err != nil {
		return err
	}
	var lastID int64
	if err = tx.Select("COALESCE(MAX(id), 0)").Scan(&lastID).Error; err != nil {
		return err
	}
	if lastID == 0 {
		return nil
	}
	tx = tx.Select("*").Where("id <= ?", lastID)
	var cursorID, cursorCreatedAt int64
	count := 0
	for {
		query := tx.Session(&gorm.Session{})
		if cursorID != 0 {
			query = query.Where("created_at < ? OR (created_at = ? AND id < ?)", cursorCreatedAt, cursorCreatedAt, cursorID)
		}
		var records []CNYBillingRecord
		if err = query.Order("created_at DESC, id DESC").Limit(256).Find(&records).Error; err != nil {
			return err
		}
		if len(records) == 0 {
			return nil
		}
		logs := make([]*Log, 0, len(records))
		for _, record := range records {
			log, err := record.usageLog()
			if err != nil {
				return err
			}
			logs = append(logs, log)
		}
		if userID != nil {
			formatUserLogs(logs, count)
		} else {
			if err = populateCNYLogChannels(logs); err != nil {
				return err
			}
			if role >= common.RoleRootUser {
				FormatRootLogs(logs)
			} else {
				FormatAdminLogs(logs)
			}
		}
		for _, log := range logs {
			if err = ctx.Err(); err != nil {
				return err
			}
			if err = fn(log); err != nil {
				return err
			}
			count++
		}
		last := records[len(records)-1]
		cursorID, cursorCreatedAt = last.ID, last.CreatedAt
	}
}

func StreamAllCNYLogs(ctx context.Context, filter LogFilter, role int, fn func(*Log) error) error {
	return streamCNYLogs(ctx, nil, filter, role, fn)
}

func StreamUserCNYLogs(ctx context.Context, userID int, filter LogFilter, fn func(*Log) error) error {
	return streamCNYLogs(ctx, &userID, filter, common.RoleCommonUser, fn)
}

type CNYLogStat struct {
	Currency     string `json:"currency"`
	Quota        int64  `json:"quota"`
	PendingQuota int64  `json:"pending_quota"`
	Count        int64  `json:"count"`
	Rpm          int64  `json:"rpm"`
	Tpm          int64  `json:"tpm"`
	QuotaPerUnit int    `json:"quota_per_unit"`
}

// A nil userID is an administrator-wide query; self APIs always pass their
// authenticated ID, including zero, so a missing identity cannot widen it.
func SumCNYUsedQuota(filter LogFilter, userID *int) (CNYLogStat, error) {
	stat := CNYLogStat{Currency: billingexpr.CurrencyCNY, QuotaPerUnit: CNYQuotaPerUnit}
	tx, err := cnyLogQuery(DB, userID, filter)
	if err != nil {
		return stat, err
	}
	tx = tx.Where("kind = ?", CNYBillingRequest)
	if err = tx.Select("COALESCE(SUM(CASE WHEN status = ? THEN quota ELSE 0 END), 0) AS quota, COALESCE(SUM(CASE WHEN status IN ? THEN reserved_quota ELSE 0 END), 0) AS pending_quota, COUNT(*) AS count", CNYBillingSettled, []string{CNYBillingReserved, CNYBillingSubmitted}).Scan(&stat).Error; err != nil {
		return stat, err
	}
	var rate struct{ Rpm, Tpm int64 }
	if err = tx.Select("COUNT(*) AS rpm, COALESCE(SUM(prompt_tokens), 0) + COALESCE(SUM(completion_tokens), 0) AS tpm").Where("created_at >= ?", common.GetTimestamp()-60).Scan(&rate).Error; err != nil {
		return stat, err
	}
	stat.Rpm, stat.Tpm = rate.Rpm, rate.Tpm
	stat.Currency, stat.QuotaPerUnit = billingexpr.CurrencyCNY, CNYQuotaPerUnit
	return stat, nil
}
