package model

import (
	"context"

	"gorm.io/gorm"
)

// LoadAssetAccessAbilities keeps material admission on the same enabled
// group/model/channel relationships used by relay routing.
func LoadAssetAccessAbilities(ctx context.Context, channelID int) ([]Ability, error) {
	return LoadAssetAccessAbilitiesWithDB(DB.WithContext(ctx), channelID)
}

func LoadAssetAccessAbilitiesWithDB(tx *gorm.DB, channelID int) ([]Ability, error) {
	var abilities []Ability
	query := tx.Where("enabled = ?", true)
	if channelID > 0 {
		query = query.Where("channel_id = ?", channelID)
	}
	err := query.Find(&abilities).Error
	return abilities, err
}
