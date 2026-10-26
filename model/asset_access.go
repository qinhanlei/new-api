package model

import "context"

// LoadAssetAccessAbilities keeps material admission on the same enabled
// group/model/channel relationships used by relay routing.
func LoadAssetAccessAbilities(ctx context.Context, channelID int) ([]Ability, error) {
	var abilities []Ability
	query := DB.WithContext(ctx).Where("enabled = ?", true)
	if channelID > 0 {
		query = query.Where("channel_id = ?", channelID)
	}
	err := query.Find(&abilities).Error
	return abilities, err
}
