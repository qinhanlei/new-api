package model

import (
	"errors"
	"os"
	"sync"
	"testing"

	"github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/types"
	"github.com/glebarez/sqlite"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"gorm.io/driver/mysql"
	"gorm.io/driver/postgres"
	"gorm.io/gorm"
	"gorm.io/gorm/schema"
)

func TestBillingRateDatabaseMatrix(t *testing.T) {
	for _, kind := range []string{"sqlite", "mysql", "postgres"} {
		t.Run(kind, func(t *testing.T) {
			var driver gorm.Dialector
			switch kind {
			case "sqlite":
				driver = sqlite.Open(":memory:")
			case "mysql":
				if os.Getenv("TEST_MYSQL_DSN") == "" {
					t.Skip("TEST_MYSQL_DSN not configured")
				}
				driver = mysqlMigrationDialector{mysql.Dialector{Config: &mysql.Config{DSN: os.Getenv("TEST_MYSQL_DSN")}}}
			case "postgres":
				if os.Getenv("TEST_POSTGRES_DSN") == "" {
					t.Skip("TEST_POSTGRES_DSN not configured")
				}
				driver = postgresMigrationDialector{postgres.Dialector{Config: &postgres.Config{DSN: os.Getenv("TEST_POSTGRES_DSN"), PreferSimpleProtocol: true}}}
			}
			db, err := gorm.Open(driver, &gorm.Config{NamingStrategy: schema.NamingStrategy{TablePrefix: "billing_rate_matrix_"}, Logger: newGormLogger(os.Stdout)})
			require.NoError(t, err)
			connection, err := db.DB()
			require.NoError(t, err)
			connection.SetMaxOpenConns(1)
			previousDB, previousType := DB, common.MainDatabaseType()
			DB = db
			common.SetMainDatabaseType(common.DatabaseType(kind))
			initCol()
			t.Cleanup(func() {
				require.NoError(t, db.Migrator().DropTable(&BillingRateRule{}, &User{}))
				require.NoError(t, connection.Close())
				DB = previousDB
				common.SetMainDatabaseType(previousType)
				initCol()
			})
			var version string
			query := "SELECT version()"
			if kind == "sqlite" {
				query = "SELECT sqlite_version()"
			}
			require.NoError(t, db.Raw(query).Scan(&version).Error)
			t.Logf("database version: %s", version)
			require.NoError(t, db.AutoMigrate(&User{}, &BillingRateRule{}))
			user := User{Username: "rate-user", Password: "fixture", Quota: 12345}
			require.NoError(t, db.Create(&user).Error)
			group := types.GroupRatioInfo{GroupRatio: 0.9}
			missing, err := ResolveBillingRate(user.Id, "Model-A", group)
			require.NoError(t, err)
			assert.Equal(t, &types.BillingRateSnapshot{ModelName: "Model-A", Rate: 0.9, Source: "group"}, missing)
			global := "0.8"
			_, _, err = UpdateBillingRateRules(0, user.Id, []BillingRateChange{{ModelName: "Model-A", Rate: &global}, {ModelName: "model-a", Rate: &global}})
			require.NoError(t, err)
			override := "0.6"
			_, changes, err := UpdateBillingRateRules(user.Id, user.Id, []BillingRateChange{{ModelName: "Model-A", Rate: &override}})
			require.NoError(t, err)
			require.Len(t, changes, 1)
			assert.Nil(t, changes[0].Before.Rate)
			assert.Equal(t, int64(0), changes[0].Before.Version)
			assert.Equal(t, "0.600000", *changes[0].After.Rate)
			assert.Equal(t, int64(1), changes[0].After.Version)
			selected, err := ResolveBillingRate(user.Id, "Model-A", types.GroupRatioInfo{GroupRatio: 0})
			require.NoError(t, err)
			assert.Equal(t, 0.6, selected.Rate, "explicit user rates replace even a free group")
			assert.Equal(t, "user", selected.Source)
			caseVariant, err := ResolveBillingRate(user.Id, "model-a", group)
			require.NoError(t, err)
			assert.Equal(t, 0.8, caseVariant.Rate)
			assert.Equal(t, "model", caseVariant.Source)
			otherUser, err := ResolveBillingRate(user.Id+1, "Model-A", group)
			require.NoError(t, err)
			assert.Equal(t, 0.8, otherUser.Rate)
			assert.Equal(t, "model", otherUser.Source)

			for index, value := range []string{"0", "1", "1.25", "100", "0.000001"} {
				_, _, err = UpdateBillingRateRules(user.Id, user.Id, []BillingRateChange{{ModelName: "Model-A", Rate: &value, ExpectedVersion: int64(index + 1)}})
				require.NoError(t, err)
				resolved, resolveErr := ResolveBillingRate(user.Id, "Model-A", group)
				require.NoError(t, resolveErr)
				assert.Equal(t, []float64{0, 1, 1.25, 100, 0.000001}[index], resolved.Rate)
			}
			cleared, _, err := UpdateBillingRateRules(user.Id, user.Id, []BillingRateChange{{ModelName: "Model-A", ExpectedVersion: 6}})
			require.NoError(t, err)
			require.Len(t, cleared.Rules, 1)
			assert.Nil(t, cleared.Rules[0].Rate)
			assert.Equal(t, int64(7), cleared.Rules[0].Version)
			require.Len(t, cleared.GlobalRules, 2)
			inherited, err := ResolveBillingRate(user.Id, "Model-A", group)
			require.NoError(t, err)
			assert.Equal(t, 0.8, inherited.Rate)
			assert.Equal(t, "model", inherited.Source)
			_, _, err = UpdateBillingRateRules(user.Id, user.Id, []BillingRateChange{{ModelName: "Model-A", Rate: &override}})
			assert.ErrorIs(t, err, ErrBillingRateConflict, "clearing retains the version and cannot be recreated by a stale editor")
			_, _, err = UpdateBillingRateRules(0, user.Id, []BillingRateChange{{ModelName: "Model-A", ExpectedVersion: 1}})
			require.NoError(t, err)
			legacy, err := ResolveBillingRate(user.Id, "Model-A", types.GroupRatioInfo{GroupRatio: 0.123456789, HasSpecialRatio: true})
			require.NoError(t, err)
			assert.Equal(t, 0.123456789, legacy.Rate, "legacy group precision must not be quantized")
			assert.Equal(t, "group_special", legacy.Source)

			_, _, err = UpdateBillingRateRules(0, user.Id, []BillingRateChange{{ModelName: "rollback-new", Rate: &global}, {ModelName: "Model-A", Rate: &global, ExpectedVersion: 1}})
			assert.ErrorIs(t, err, ErrBillingRateConflict)
			var count int64
			require.NoError(t, db.Model(&BillingRateRule{}).Where("model_key = ?", billingRateModelKey("rollback-new")).Count(&count).Error)
			assert.Zero(t, count, "one conflict rolls back the entire batch")
			for _, value := range []string{"-1", "NaN", "Inf", "100.000001", "1.0000001", "1e2", "", " 0.5"} {
				_, _, err := UpdateBillingRateRules(0, user.Id, []BillingRateChange{{ModelName: "invalid-rate", Rate: &value}})
				assert.Error(t, err, value)
			}
			_, _, err = UpdateBillingRateRules(user.Id+9999, user.Id, []BillingRateChange{{ModelName: "Model-A", Rate: &global}})
			assert.ErrorIs(t, err, ErrBillingRateUser)
			_, _, err = UpdateBillingRateRules(0, user.Id, []BillingRateChange{{ModelName: "duplicate"}, {ModelName: "duplicate"}})
			assert.Error(t, err)

			concurrentErrors := make(chan error, 2)
			var writers sync.WaitGroup
			for range 2 {
				writers.Go(func() {
					_, _, err := UpdateBillingRateRules(0, user.Id, []BillingRateChange{{ModelName: "concurrent", Rate: &global}})
					concurrentErrors <- err
				})
			}
			writers.Wait()
			close(concurrentErrors)
			committed, conflicts := 0, 0
			for err := range concurrentErrors {
				if err == nil {
					committed++
				} else if errors.Is(err, ErrBillingRateConflict) {
					conflicts++
				} else {
					require.NoError(t, err)
				}
			}
			assert.Equal(t, 1, committed)
			assert.Equal(t, 1, conflicts)
			for range 2 {
				require.NoError(t, db.AutoMigrate(&BillingRateRule{}))
			}
			persisted, err := GetBillingRateRules(user.Id)
			require.NoError(t, err)
			assert.Equal(t, cleared.Rules, persisted.Rules)
			require.True(t, db.Migrator().HasIndex(&BillingRateRule{}, "idx_billing_rate_owner_model"))
			var preservedUser User
			require.NoError(t, db.First(&preservedUser, user.Id).Error)
			assert.Equal(t, user.Quota, preservedUser.Quota)
			require.NoError(t, db.Migrator().DropTable(&BillingRateRule{}))
			_, err = ResolveBillingRate(user.Id, "Model-A", group)
			assert.ErrorIs(t, err, ErrBillingRateUnavailable, "a database failure must not silently change the price")
		})
	}
}

func TestBillingRatesAreRemovedWithUser(t *testing.T) {
	truncateTables(t)
	for _, hard := range []bool{false, true} {
		user := User{Username: "rate-delete", AffCode: "rate-delete", Password: "fixture"}
		if hard {
			user.Username, user.AffCode = "rate-delete-hard", "rate-delete-hard"
		}
		require.NoError(t, DB.Create(&user).Error)
		rate := "0.6"
		_, _, err := UpdateBillingRateRules(user.Id, user.Id, []BillingRateChange{{ModelName: "model-a", Rate: &rate}})
		require.NoError(t, err)
		if hard {
			_, err = user.HardDelete()
		} else {
			_, err = user.Delete()
		}
		require.NoError(t, err)
		var count int64
		require.NoError(t, DB.Model(&BillingRateRule{}).Where("owner_user_id = ?", user.Id).Count(&count).Error)
		assert.Zero(t, count)
		_, _, err = UpdateBillingRateRules(user.Id, 1, []BillingRateChange{{ModelName: "model-b", Rate: &rate}})
		assert.ErrorIs(t, err, ErrBillingRateUser)
	}
}
