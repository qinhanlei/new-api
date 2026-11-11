package model

import (
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/QuantumNous/new-api/common"
	"github.com/glebarez/sqlite"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"gorm.io/driver/mysql"
	"gorm.io/driver/postgres"
	"gorm.io/gorm"
	"gorm.io/gorm/schema"
)

func TestWalletExchangeDatabaseMatrix(t *testing.T) {
	for _, dialect := range []common.DatabaseType{common.DatabaseTypeSQLite, common.DatabaseTypeMySQL, common.DatabaseTypePostgreSQL} {
		t.Run(string(dialect), func(t *testing.T) {
			var driver gorm.Dialector
			switch dialect {
			case common.DatabaseTypeSQLite:
				driver = sqlite.Open(filepath.Join(t.TempDir(), "exchange.db"))
			case common.DatabaseTypeMySQL:
				dsn := os.Getenv("TEST_MYSQL_DSN")
				if dsn == "" {
					t.Skip("TEST_MYSQL_DSN is not configured")
				}
				driver = mysql.Open(dsn)
			case common.DatabaseTypePostgreSQL:
				dsn := os.Getenv("TEST_POSTGRES_DSN")
				if dsn == "" {
					t.Skip("TEST_POSTGRES_DSN is not configured")
				}
				driver = postgres.Open(dsn)
			}
			db, err := gorm.Open(driver, &gorm.Config{NamingStrategy: schema.NamingStrategy{TablePrefix: "fx_policy_matrix_"}})
			require.NoError(t, err)
			previousDB, previousType := DB, common.MainDatabaseType()
			DB = db
			common.SetMainDatabaseType(dialect)
			t.Cleanup(func() {
				DB = previousDB
				common.SetMainDatabaseType(previousType)
				sqlDB, _ := db.DB()
				_ = sqlDB.Close()
			})
			require.NoError(t, db.Migrator().DropTable(&WalletExchangePolicy{}, &ExchangeRateObservation{}))
			for range 2 {
				require.NoError(t, db.AutoMigrate(&WalletExchangePolicy{}, &ExchangeRateObservation{}))
			}

			policy, err := GetWalletExchangePolicy()
			require.NoError(t, err)
			assert.False(t, policy.CrossCurrencyEnabled)
			assert.False(t, policy.DynamicEnabled)
			policy.StaticRate, policy.CrossCurrencyEnabled = "6.7", true
			saved, err := SaveWalletExchangePolicy(*policy, nil)
			require.NoError(t, err)
			_, err = SaveWalletExchangePolicy(*policy, nil)
			assert.ErrorIs(t, err, ErrExchangePolicyConflict)
			now := time.Now().Unix()
			snapshot, err := GetWalletExchangeSnapshot(db, now)
			require.NoError(t, err)
			assert.Equal(t, "6.7", snapshot.Rate)
			assert.Equal(t, "manual", snapshot.Source)
			assert.True(t, snapshot.Enabled)
			assert.Equal(t, int64(1), snapshot.PolicyVersion)

			saved.DynamicEnabled = true
			_, err = SaveWalletExchangePolicy(*saved, nil)
			assert.ErrorIs(t, err, ErrExchangeRateUnavailable)
			baseline := &ExchangeRateObservation{Rate: "6.7", Source: "frankfurter", SourceTime: now - 3600}
			saved, err = SaveWalletExchangePolicy(*saved, baseline)
			require.NoError(t, err)
			accepted, err := RecordExchangeObservation(saved.Version, ExchangeRateObservation{Rate: "6.75", Source: "frankfurter", SourceTime: now})
			require.NoError(t, err)
			assert.True(t, accepted.Accepted)
			for _, candidate := range []ExchangeRateObservation{
				{Rate: "2", SourceTime: now},
				{Rate: "6.92", SourceTime: now}, // below 3% from 6.75, above 3% from today's 6.7 baseline
				{Rate: "6.75", SourceTime: now - 7200},
				{Rate: "6.75", SourceTime: now + 7200},
				{Rate: "6.75", SourceTime: now - 8*86400},
				{Rate: "NaN", SourceTime: now},
				{Rate: "1e999999999", SourceTime: now},
			} {
				candidate.Source = "frankfurter"
				row, err := RecordExchangeObservation(saved.Version, candidate)
				require.NoError(t, err)
				assert.False(t, row.Accepted, candidate)
			}
			snapshot, err = GetWalletExchangeSnapshot(db, now)
			require.NoError(t, err)
			assert.Equal(t, "6.75", snapshot.Rate)
			assert.Equal(t, now, snapshot.SourceTime)
			_, err = GetWalletExchangeSnapshot(db, now+8*86400)
			assert.ErrorIs(t, err, ErrExchangeRateUnavailable)
			saved.DynamicEnabled = false
			saved, err = SaveWalletExchangePolicy(*saved, nil)
			require.NoError(t, err)
			_, err = RecordExchangeObservation(saved.Version-1, *baseline)
			assert.ErrorIs(t, err, ErrExchangePolicyConflict)
			snapshot, err = GetWalletExchangeSnapshot(db, now)
			require.NoError(t, err)
			assert.Equal(t, "6.7", snapshot.Rate)
			for range 2 {
				require.NoError(t, db.AutoMigrate(&WalletExchangePolicy{}, &ExchangeRateObservation{}))
			}
			reloaded, err := GetWalletExchangePolicy()
			require.NoError(t, err)
			assert.Equal(t, saved, reloaded)

			// Daily moves cannot compound past the weekly bound. The dates
			// represent accepted history, independent of today's fetch time.
			saved.DynamicEnabled = true
			saved, err = SaveWalletExchangePolicy(*saved, &ExchangeRateObservation{Rate: "6.7", Source: "frankfurter", SourceTime: now - 6*86400})
			require.NoError(t, err)
			require.NoError(t, db.Model(&ExchangeRateObservation{}).Where("id = ?", saved.BaselineRateID).Update("observed_at", now-6*86400).Error)
			previous := ExchangeRateObservation{Rate: "7.3", Source: "frankfurter", SourceTime: now - 2*86400, ObservedAt: now - 2*86400, PolicyVersion: saved.Version, Accepted: true}
			require.NoError(t, db.Create(&previous).Error)
			require.NoError(t, db.Model(&WalletExchangePolicy{}).Where("id = ?", 1).Update("active_rate_id", previous.ID).Error)
			rejected, err := RecordExchangeObservation(saved.Version, ExchangeRateObservation{Rate: "7.5", Source: "frankfurter", SourceTime: now})
			require.NoError(t, err)
			assert.False(t, rejected.Accepted)
			assert.Contains(t, rejected.Reason, "10%")
			refetched, err := RecordExchangeObservation(saved.Version, ExchangeRateObservation{Rate: "7.36", Source: "frankfurter", SourceTime: previous.SourceTime})
			require.NoError(t, err)
			assert.True(t, refetched.Accepted)
			snapshot, err = GetWalletExchangeSnapshot(db, now)
			require.NoError(t, err)
			assert.Equal(t, previous.SourceTime, snapshot.SourceTime)
			_, err = GetWalletExchangeSnapshot(db, previous.SourceTime+int64(saved.MaxQuoteAgeHours)*3600+1)
			assert.ErrorIs(t, err, ErrExchangeRateUnavailable, "a fresh fetch must not extend the source quote's lifetime")
		})
	}
}
