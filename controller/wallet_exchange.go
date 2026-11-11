package controller

import (
	"errors"
	"net/http"

	"github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/model"
	"github.com/QuantumNous/new-api/service"
	"github.com/gin-gonic/gin"
	"gorm.io/gorm"
)

func GetWalletExchangeSettings(c *gin.Context) {
	policy, err := model.GetWalletExchangePolicy()
	if err != nil {
		common.ApiError(c, err)
		return
	}
	var latest model.ExchangeRateObservation
	err = model.DB.Order("id DESC").First(&latest).Error
	if err != nil && !errors.Is(err, gorm.ErrRecordNotFound) {
		common.ApiError(c, err)
		return
	}
	snapshot, snapshotErr := model.GetWalletExchangeSnapshot(model.DB, common.GetTimestamp())
	status := "ready"
	if snapshotErr != nil {
		status = "unavailable"
	} else if policy.DynamicEnabled && latest.ID > 0 && !latest.Accepted {
		status = "last_good"
	}
	common.ApiSuccess(c, gin.H{"policy": policy, "rate": snapshot, "latest_observation": latest, "status": status})
}

func PreviewWalletExchangeRate(c *gin.Context) {
	row, err := service.FetchWalletExchangeRate(c.Request.Context())
	if err != nil {
		common.ApiErrorT(c, "Failed to fetch exchange rate")
		return
	}
	common.ApiSuccess(c, row)
}

func UpdateWalletExchangeSettings(c *gin.Context) {
	var request struct {
		Policy model.WalletExchangePolicy `json:"policy"`
		// The browser sends the preview it displayed. The server re-fetches it
		// before accepting the baseline, rather than trusting a client rate.
		BaselineRate string `json:"baseline_rate"`
	}
	if err := c.ShouldBindJSON(&request); err != nil {
		common.ApiErrorStatus(c, http.StatusBadRequest, common.NewMessage("Invalid parameters"))
		return
	}
	if err := model.ValidateWalletExchangePolicy(request.Policy); err != nil {
		common.ApiErrorStatus(c, http.StatusBadRequest, common.NewMessage(err.Error()))
		return
	}
	current, err := model.GetWalletExchangePolicy()
	if err != nil {
		common.ApiError(c, err)
		return
	}
	var baseline *model.ExchangeRateObservation
	if request.Policy.DynamicEnabled && !current.DynamicEnabled {
		baseline, err = service.FetchWalletExchangeRate(c.Request.Context())
		if err != nil || baseline.Rate != request.BaselineRate {
			common.ApiErrorStatus(c, http.StatusConflict, common.NewMessage("Exchange rate preview changed; reload before saving"))
			return
		}
	}
	saved, err := model.SaveWalletExchangePolicy(request.Policy, baseline)
	if err != nil {
		status := http.StatusBadRequest
		if errors.Is(err, model.ErrExchangePolicyConflict) {
			status = http.StatusConflict
		}
		common.ApiErrorStatus(c, status, common.NewMessage(err.Error()))
		return
	}
	model.RecordOperationAuditLog(c.GetInt("id"), c.GetInt("role"), "Currency settings updated", c.ClientIP(), "billing.currency_settings", model.AuditFields{"version": saved.Version, "cross_currency_enabled": saved.CrossCurrencyEnabled, "dynamic_enabled": saved.DynamicEnabled, "static_rate": saved.StaticRate, "interval_hours": saved.IntervalHours}, auditOperatorInfo(c), nil, c)
	markAuditLogged(c)
	GetWalletExchangeSettings(c)
}
