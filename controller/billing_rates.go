package controller

import (
	"encoding/json"
	"errors"
	"net/http"
	"net/url"
	"strconv"

	"github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/model"
	"github.com/gin-gonic/gin"
)

func billingRateOwner(c *gin.Context) (int, bool) {
	if c.Param("id") == "" {
		return 0, true
	}
	owner, err := strconv.Atoi(c.Param("id"))
	if err != nil || owner <= 0 {
		common.ApiErrorStatus(c, http.StatusBadRequest, model.ErrBillingRateUser)
		return 0, false
	}
	return owner, true
}

func billingRateQueryModel(c *gin.Context) (string, error) {
	query, err := url.ParseQuery(c.Request.URL.RawQuery)
	if err != nil {
		return "", common.NewMessage("Invalid billing rate model name")
	}
	names, exists := query["model_name"]
	if !exists {
		return "", nil
	}
	if len(names) != 1 {
		return "", common.NewMessage("Invalid billing rate model name")
	}
	if err := model.ValidateBillingRateModelName(names[0]); err != nil {
		return "", err
	}
	return model.ResolveBillingModelName(names[0]), nil
}

func GetBillingRates(c *gin.Context) {
	owner, ok := billingRateOwner(c)
	if !ok {
		return
	}
	billingModelName, err := billingRateQueryModel(c)
	if err != nil {
		common.ApiErrorStatus(c, http.StatusBadRequest, err)
		return
	}
	rules, err := model.GetBillingRateRules(owner)
	if err != nil {
		if errors.Is(err, model.ErrBillingRateUser) {
			common.ApiErrorStatus(c, http.StatusNotFound, err)
		} else {
			common.ApiErrorStatus(c, http.StatusInternalServerError, common.NewMessage("Unable to load billing rates"))
		}
		return
	}
	rules.BillingModelName = billingModelName
	common.ApiSuccess(c, rules)
}

func UpdateBillingRates(c *gin.Context) {
	owner, ok := billingRateOwner(c)
	if !ok {
		return
	}
	billingModelName, err := billingRateQueryModel(c)
	if err != nil {
		common.ApiErrorStatus(c, http.StatusBadRequest, err)
		return
	}
	var request struct {
		Changes []struct {
			ModelName       string          `json:"model_name"`
			Rate            json.RawMessage `json:"rate"`
			ExpectedVersion *int64          `json:"expected_version"`
		} `json:"changes"`
	}
	if err := common.DecodeJson(http.MaxBytesReader(c.Writer, c.Request.Body, 256*1024), &request); err != nil {
		common.ApiErrorStatus(c, http.StatusBadRequest, common.NewMessage("Invalid parameters"))
		return
	}
	changes := make([]model.BillingRateChange, 0, len(request.Changes))
	for _, change := range request.Changes {
		// Omitted rate must not silently clear an existing rule. Only an explicit
		// JSON null requests inheritance; numeric input must be a decimal string.
		if len(change.Rate) == 0 || change.ExpectedVersion == nil {
			common.ApiErrorStatus(c, http.StatusBadRequest, common.NewMessage("Billing rate and expected version are required"))
			return
		}
		var rate *string
		if err := common.Unmarshal(change.Rate, &rate); err != nil {
			common.ApiErrorStatus(c, http.StatusBadRequest, common.NewMessage("Billing rate must be between 0 and 100 with at most 6 decimal places"))
			return
		}
		changes = append(changes, model.BillingRateChange{ModelName: change.ModelName, Rate: rate, ExpectedVersion: *change.ExpectedVersion})
	}
	rules, audit, err := model.UpdateBillingRateRules(owner, c.GetInt("id"), changes)
	if err != nil {
		status := http.StatusBadRequest
		var message *common.Message
		switch {
		case errors.Is(err, model.ErrBillingRateConflict):
			status = http.StatusConflict
		case errors.Is(err, model.ErrBillingRateUser):
			status = http.StatusNotFound
		case !errors.As(err, &message):
			status = http.StatusInternalServerError
			err = common.NewMessage("Unable to save billing rates")
		}
		common.ApiErrorStatus(c, status, err)
		return
	}
	rules.BillingModelName = billingModelName
	recordManageAuditFor(c, owner, "billing.rates.update", map[string]any{"owner_user_id": owner, "changes": audit})
	common.ApiSuccess(c, rules)
}
