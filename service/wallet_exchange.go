package service

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"time"

	"github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/logger"
	"github.com/QuantumNous/new-api/model"
)

const exchangeRefreshTask = "wallet_exchange_refresh"

// FetchWalletExchangeRate uses a fixed endpoint: admin settings cannot redirect
// the scheduler to internal services. The rate is a reference, not a live quote.
func FetchWalletExchangeRate(ctx context.Context) (*model.ExchangeRateObservation, error) {
	ctx, cancel := context.WithTimeout(ctx, 12*time.Second)
	defer cancel()
	request, err := http.NewRequestWithContext(ctx, http.MethodGet, "https://api.frankfurter.dev/v2/rate/USD/CNY", nil)
	if err != nil {
		return nil, err
	}
	client := &http.Client{Timeout: 12 * time.Second, CheckRedirect: func(_ *http.Request, _ []*http.Request) error { return http.ErrUseLastResponse }}
	response, err := client.Do(request)
	if err != nil {
		return nil, err
	}
	defer response.Body.Close()
	if response.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("exchange rate provider returned HTTP %d", response.StatusCode)
	}
	var result struct {
		Date  string      `json:"date"`
		Base  string      `json:"base"`
		Quote string      `json:"quote"`
		Rate  json.Number `json:"rate"`
	}
	if err := common.DecodeJson(io.LimitReader(response.Body, 16384), &result); err != nil {
		return nil, errors.New("Invalid exchange rate response")
	}
	date, err := time.Parse("2006-01-02", result.Date)
	if err != nil || result.Base != "USD" || result.Quote != "CNY" {
		return nil, errors.New("Invalid exchange rate currency or date")
	}
	return &model.ExchangeRateObservation{Rate: result.Rate.String(), Source: "frankfurter", SourceTime: date.Unix()}, nil
}

type walletExchangeRefreshHandler struct{}

func (walletExchangeRefreshHandler) Type() string { return exchangeRefreshTask }
func (walletExchangeRefreshHandler) Enabled() bool {
	policy, err := model.GetWalletExchangePolicy()
	return err == nil && policy.DynamicEnabled
}
func (walletExchangeRefreshHandler) Interval() time.Duration {
	policy, err := model.GetWalletExchangePolicy()
	if err != nil || policy.IntervalHours < 1 {
		return 6 * time.Hour
	}
	return time.Duration(policy.IntervalHours) * time.Hour
}
func (walletExchangeRefreshHandler) NewPayload() any { return struct{}{} }
func (walletExchangeRefreshHandler) Run(ctx context.Context, task *model.SystemTask, runnerID string) {
	row, err := RefreshWalletExchangeRate(ctx)
	status, message := model.SystemTaskStatusSucceeded, ""
	if err != nil {
		status, message = model.SystemTaskStatusFailed, err.Error()
		logger.LogWarn(ctx, "Wallet exchange rate refresh failed: "+message)
	}
	if finishErr := model.FinishSystemTask(task.TaskID, runnerID, status, row, message); finishErr != nil {
		logger.LogWarn(ctx, "Wallet exchange rate task completion failed: "+finishErr.Error())
	}
}

func RefreshWalletExchangeRate(ctx context.Context) (*model.ExchangeRateObservation, error) {
	policy, err := model.GetWalletExchangePolicy()
	if err != nil {
		return nil, err
	}
	if !policy.DynamicEnabled {
		return nil, model.ErrExchangePolicyConflict
	}
	row, fetchErr := FetchWalletExchangeRate(ctx)
	if fetchErr != nil {
		row = &model.ExchangeRateObservation{Source: "frankfurter", Reason: "Exchange rate provider is unavailable"}
	}
	observation, err := model.RecordExchangeObservation(policy.Version, *row)
	if err != nil {
		return observation, err
	}
	if fetchErr != nil {
		return observation, fetchErr
	}
	if !observation.Accepted {
		return observation, errors.New(observation.Reason)
	}
	return observation, nil
}

func init() { RegisterSystemTaskHandler(walletExchangeRefreshHandler{}) }
